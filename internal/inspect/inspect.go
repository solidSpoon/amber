// Package inspect 在 cdp 之上实现「验收真正需要的几个动作」：
// 读页面结构、按引用点击、输入、按键、截图。
//
// 设计原则：
//   - 每个动作都返回结构化结果，方便控制面把参数/结果写进审计；
//   - 元素引用号（ref）由快照分配并在下一次快照前保持有效，避免让模型拼选择器；
//   - 截图落盘由调用方给路径，本包不关心目录结构。
package inspect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/solidSpoon/rigd/internal/cdp"
)

// Element 页面上一个可交互/可读元素。
type Element struct {
	// Ref 稳定引用号（如 "e7"），本次快照内有效。
	Ref string `json:"ref"`
	// Role 语义角色：button/link/textbox/heading/video…
	Role string `json:"role"`
	// Name 可读名称（aria-label、placeholder、文本内容）。
	Name string `json:"name,omitempty"`
	// TestID data-testid / data-slot，最稳的定位依据。
	TestID string `json:"testid,omitempty"`
	// Box 元素矩形 [x, y, w, h]，屏幕坐标。
	Box [4]int `json:"box"`
}

// Snapshot 一次页面快照。
type Snapshot struct {
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	Elements []Element `json:"elements"`
	Text     string    `json:"text"`
}

// Client 会话级的检查器：一个 Client 绑定一个容器的调试端口。
type Client struct {
	cdp  *cdp.Client
	refs map[string]Element
}

// Factory 按端口创建检查器；抽出接口是为了让调度器可以在测试里注入假实现。
type Factory func(ctx context.Context, port int) (*Client, error)

// Dial 连到一个已经就绪的调试端口。
func Dial(ctx context.Context, port int) (*Client, error) {
	// 冷启动要 clone + 装依赖 + 构建 + 启动，5 分钟是实测最坏情况的上限
	conn, err := cdp.DialPage(ctx, port, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &Client{cdp: conn, refs: map[string]Element{}}, nil
}

// Close 断开连接。
func (c *Client) Close() error { return c.cdp.Close() }

// snapshotScript 收集页面上值得让 AI 看到的东西。
//
// 刻意不 dump 整棵 DOM：播放器/编辑器类界面的 DOM 动辄上万节点，塞进上下文既贵又噪声大。
const snapshotScript = `(() => {
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 1 && r.height > 1 && s.visibility !== 'hidden' && s.display !== 'none';
  };
  const roleOf = (el) => {
    const explicit = el.getAttribute('role');
    if (explicit) return explicit;
    const tag = el.tagName.toLowerCase();
    if (tag === 'button') return 'button';
    if (tag === 'a') return 'link';
    if (tag === 'input') return el.type === 'text' || !el.type ? 'textbox' : el.type;
    if (tag === 'textarea') return 'textbox';
    if (tag === 'video') return 'video';
    if (/^h[1-6]$/.test(tag)) return 'heading';
    if (tag === 'select') return 'combobox';
    return tag;
  };
  const nameOf = (el) => (el.getAttribute('aria-label') || el.getAttribute('placeholder')
    || el.getAttribute('title') || (el.innerText || '').replace(/\s+/g, ' ').trim()
    || el.value || '').slice(0, 120);
  const sel = 'button,a[href],input,textarea,select,[role],[data-testid],[data-slot],h1,h2,h3,h4,video,[contenteditable="true"]';
  const out = [];
  for (const el of document.querySelectorAll(sel)) {
    if (!visible(el)) continue;
    const r = el.getBoundingClientRect();
    out.push({
      role: roleOf(el),
      name: nameOf(el),
      testid: el.getAttribute('data-testid') || el.getAttribute('data-slot') || '',
      box: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)],
    });
  }
  return {
    url: location.href,
    title: document.title,
    elements: out.slice(0, 120),
    text: (document.body.innerText || '').replace(/\n{2,}/g, '\n').trim().slice(0, 2000),
  };
})()`

// Snapshot 读页面结构，并为每个元素分配引用号。
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	raw, err := c.cdp.Evaluate(ctx, snapshotScript)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("解析页面快照失败: %w", err)
	}

	// 引用号按「先 role 后名称」排序后分配，保证同一次快照内稳定、可复现
	sort.SliceStable(snap.Elements, func(i, j int) bool {
		if snap.Elements[i].Role != snap.Elements[j].Role {
			return snap.Elements[i].Role < snap.Elements[j].Role
		}
		return snap.Elements[i].Name < snap.Elements[j].Name
	})
	c.refs = make(map[string]Element, len(snap.Elements))
	for i := range snap.Elements {
		ref := fmt.Sprintf("e%d", i+1)
		snap.Elements[i].Ref = ref
		c.refs[ref] = snap.Elements[i]
	}
	return snap, nil
}

// Resolve 把引用号还原成元素；引用失效时明确报错，避免点到别的地方。
func (c *Client) Resolve(ref string) (Element, error) {
	el, ok := c.refs[ref]
	if !ok {
		return Element{}, fmt.Errorf("元素引用 %q 已失效，请重新 snapshot", ref)
	}
	return el, nil
}

// Click 点击引用号对应的元素（先滚动到可见，再按中心点派发真实鼠标事件）。
func (c *Client) Click(ctx context.Context, ref string) error {
	el, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	x := float64(el.Box[0]) + float64(el.Box[2])/2
	y := float64(el.Box[1]) + float64(el.Box[3])/2
	return c.cdp.ClickAt(ctx, x, y)
}

// ClickAt 按坐标点击（兜底：原生弹窗、自绘控件）。
func (c *Client) ClickAt(ctx context.Context, x, y float64) error {
	return c.cdp.ClickAt(ctx, x, y)
}

// Fill 往输入框里填文本：先聚焦、全选、再用真实输入事件替换内容。
func (c *Client) Fill(ctx context.Context, ref, text string) error {
	el, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	x := float64(el.Box[0]) + float64(el.Box[2])/2
	y := float64(el.Box[1]) + float64(el.Box[3])/2
	if err := c.cdp.ClickAt(ctx, x, y); err != nil {
		return err
	}
	// Ctrl+A 选中已有内容，再插入文本，等价于用户全选覆盖
	if err := c.cdp.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyDown", "key": "a", "modifiers": 2, "windowsVirtualKeyCode": 65,
	}, nil); err != nil {
		return err
	}
	if err := c.cdp.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": "a", "modifiers": 2, "windowsVirtualKeyCode": 65,
	}, nil); err != nil {
		return err
	}
	return c.cdp.TypeText(ctx, text)
}

// Press 发送一次按键。
func (c *Client) Press(ctx context.Context, key string) error {
	return c.cdp.KeyPress(ctx, key)
}

// Evaluate 执行只读 JS 并返回原始 JSON。
func (c *Client) Evaluate(ctx context.Context, script string) (json.RawMessage, error) {
	return c.cdp.Evaluate(ctx, script)
}

// PreviewJPEG 只取一帧 JPEG（不落盘），给看板的实时预览用。
func (c *Client) PreviewJPEG(ctx context.Context, quality int) ([]byte, error) {
	data, err := c.cdp.ScreenshotJPEG(ctx, quality)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("解码预览帧失败: %w", err)
	}
	return raw, nil
}

// Screenshot 截图并落盘，返回写入的字节数。
func (c *Client) Screenshot(ctx context.Context, path string) (int64, error) {
	data, err := c.cdp.Screenshot(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return 0, fmt.Errorf("解码截图失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("创建截图目录失败: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return 0, fmt.Errorf("写入截图失败: %w", err)
	}
	return int64(len(raw)), nil
}

// Summarize 把快照压成一段适合塞进上下文的文本。
func Summarize(s Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nTITLE: %s\n\nELEMENTS:\n", s.URL, s.Title)
	for _, el := range s.Elements {
		fmt.Fprintf(&b, "%s %s", el.Ref, el.Role)
		if el.TestID != "" {
			fmt.Fprintf(&b, " testid=%s", el.TestID)
		}
		if el.Name != "" {
			fmt.Fprintf(&b, " name=%q", el.Name)
		}
		fmt.Fprintf(&b, " box=%v\n", el.Box)
	}
	b.WriteString("\nVISIBLE TEXT:\n")
	b.WriteString(s.Text)
	return b.String()
}
