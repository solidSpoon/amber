// Package inspect 在 cdp 之上实现「验收真正需要的几个动作」：
// 读页面结构、按引用点击、输入、按键、框选元素。
//
// 设计原则：
//   - 每个动作都返回结构化结果，方便控制面把参数/结果写进审计；
//   - 元素引用号（ref）由快照分配并在下一次快照前保持有效，避免让模型拼选择器；
//   - 截图不在这里拍：证据是整块屏幕（含原生对话框），由 session 层对着虚拟屏抓帧。
package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// nextQuery 给「按文字/选择器现找」的元素编号（q1、q2…），
	// 和 snapshot 的 eN 共用一个表，后续操作不用区分来源。
	nextQuery int
}

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

// jsElementHelpers 是"页面上哪些元素算数"的唯一一份判断：快照用它列元素，
// 按文字/选择器定位也用它找元素。
//
// 两份各写一遍的后果很具体：快照里列得出的元素，定位却找不到（或者反过来），
// 模型拿着引用号点不动，只能猜。
const jsElementHelpers = `  const SEL = 'button,a[href],input,textarea,select,[role],[data-testid],[data-slot],h1,h2,h3,h4,video,[contenteditable="true"]';
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
  const boxOf = (el) => {
    const r = el.getBoundingClientRect();
    return [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)];
  };`

// snapshotScript 收集页面上值得让 AI 看到的东西。
//
// 刻意不 dump 整棵 DOM：播放器/编辑器类界面的 DOM 动辄上万节点，塞进上下文既贵又噪声大。
const snapshotScript = `(() => {
` + jsElementHelpers + `
  const out = [];
  for (const el of document.querySelectorAll(SEL)) {
    if (!visible(el)) continue;
    out.push({
      role: roleOf(el),
      name: nameOf(el),
      testid: el.getAttribute('data-testid') || el.getAttribute('data-slot') || '',
      box: boxOf(el),
    });
  }
  return {
    url: location.href,
    title: document.title,
    elements: out.slice(0, 120),
    text: (document.body.innerText || '').replace(/\n{2,}/g, '\n').trim().slice(0, 2000),
  };
})()`

// locateScript 按「可见文字」或「CSS 选择器」现找元素。
//
// 为什么需要它：引用号是某一次 snapshot 的产物，页面一变就作废。批里
// 「点完这步、页面翻页、再点下一步」这种连招，根本没法事先写出引用号。
const locateScript = `(() => {
` + jsElementHelpers + `
  const wanted = %s;
  const selector = %s;
  const candidates = [...document.querySelectorAll(SEL)].filter(visible);
  let el = null;
  if (selector) {
    el = candidates.find((e) => e.matches(selector)) || document.querySelector(selector);
  } else {
    el = candidates.find((e) => nameOf(e) === wanted) || candidates.find((e) => nameOf(e).includes(wanted));
  }
  if (!el) return null;
  return { role: roleOf(el), name: nameOf(el), box: boxOf(el) };
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

// Locate 按文字或选择器找元素，并登记成一个引用号。
//
// 登记之后它和 snapshot 给的引用完全一样：点击、填写、截图高亮、记录里那句
// 「点击「Skip」按钮」都走同一条路，不会多出第二套"按文字点"的实现。
func (c *Client) Locate(ctx context.Context, match, selector string) (string, error) {
	if match == "" && selector == "" {
		return "", errors.New("需要 match（可见文字）或 selector（CSS 选择器）之一")
	}
	matchJSON, _ := json.Marshal(match)
	selectorJSON, _ := json.Marshal(selector)
	script := fmt.Sprintf(locateScript, matchJSON, selectorJSON)
	raw, err := c.cdp.Evaluate(ctx, script)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) == "null" {
		what := match
		if selector != "" {
			what = selector
		}
		return "", fmt.Errorf("页面上找不到「%s」", what)
	}
	var el Element
	if err := json.Unmarshal(raw, &el); err != nil {
		return "", fmt.Errorf("解析定位结果失败: %w", err)
	}
	c.nextQuery++
	ref := fmt.Sprintf("q%d", c.nextQuery)
	el.Ref = ref
	c.refs[ref] = el
	return ref, nil
}

// center 元素中心点的页面坐标：点、悬停、填写都按它算。
func (e Element) center() (float64, float64) {
	return float64(e.Box[0]) + float64(e.Box[2])/2, float64(e.Box[1]) + float64(e.Box[3])/2
}

// Click 点击引用号对应的元素（先滚动到可见，再按中心点派发真实鼠标事件）。
func (c *Client) Click(ctx context.Context, ref string) error {
	el, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	x, y := el.center()
	return c.cdp.ClickAt(ctx, x, y)
}

// Hover 把鼠标移到元素上（悬停才会出现的浮层用它）。
//
// 连发两次移动：第一次让元素进入 hover 状态，页面此时才会挂上自己的
// mousemove 监听；只发一次的话，监听还没生效，浮层就不会出来。
func (c *Client) Hover(ctx context.Context, ref string) error {
	el, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	x, y := el.center()
	// 走出一点轨迹：悬停浮层通常要连续几个 mousemove 才判定"停在了这个元素上"，
	// 一步跳进去往往只触发一次 mouseover，随后就被判成移出。
	for _, dx := range []float64{-12, -6, 0, 6, 12} {
		if err := c.cdp.MouseMove(ctx, x+dx, y); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}
	return nil
}

// HoverAt 把鼠标移到页面坐标上。
func (c *Client) HoverAt(ctx context.Context, x, y float64) error {
	return c.cdp.MouseMove(ctx, x, y)
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
	x, y := el.center()
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

// Viewport 应用窗口内部的可绘制尺寸（用来校验窗口是否铺满虚拟屏）。
func (c *Client) Viewport(ctx context.Context) (int, int, error) {
	return c.cdp.Viewport(ctx)
}

// Evaluate 执行只读 JS 并返回原始 JSON。
func (c *Client) Evaluate(ctx context.Context, script string) (json.RawMessage, error) {
	return c.cdp.Evaluate(ctx, script)
}

// Summarize 把快照压成一段适合塞进上下文的文本。
func Summarize(s Snapshot) string {
	var b strings.Builder
	// 这里只写人看得懂的东西：页面叫什么、有哪些能点的元素。
	// 地址栏那种路径信息对读者没有意义，记录里出现只会让人分心。
	fmt.Fprintf(&b, "页面：%s（%d 个可操作元素）\n\n", s.Title, len(s.Elements))
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

// Highlight 在页面上把某个元素框出来（Steps Recorder 那个红圈）。
//
// 框是页面里的一个覆盖层，所以整屏抓帧会把它一起拍进去。
func (c *Client) Highlight(ctx context.Context, ref string) error {
	el, err := c.Resolve(ref)
	if err != nil {
		return err
	}
	if _, err := c.cdp.Evaluate(ctx, highlightScript(el)); err != nil {
		return fmt.Errorf("框选元素失败: %w", err)
	}
	return nil
}

// ClearHighlight 撤掉框。调用方自己决定什么时候收尾（抓完帧）。
func (c *Client) ClearHighlight(ctx context.Context) error {
	_, err := c.cdp.Evaluate(ctx, clearHighlightScript)
	return err
}

// Describe 取引用对应的元素（给这一步自动写一句人话用）。
func (c *Client) Describe(ref string) (Element, bool) {
	el, ok := c.refs[ref]
	return el, ok
}

func highlightScript(el Element) string {
	x, y, w, h := el.Box[0], el.Box[1], el.Box[2], el.Box[3]
	return fmt.Sprintf(`(() => {
  const old = document.getElementById('__rigd_hl');
  if (old) old.remove();
  const d = document.createElement('div');
  d.id = '__rigd_hl';
  Object.assign(d.style, {
    position: 'fixed', left: '%dpx', top: '%dpx', width: '%dpx', height: '%dpx',
    border: '2px solid #ef4444', borderRadius: '6px',
    boxShadow: '0 0 0 3px rgba(239,68,68,0.25)',
    zIndex: 2147483647, pointerEvents: 'none',
  });
  document.body.appendChild(d);
  return true;
})()`, x, y, w, h)
}

const clearHighlightScript = `(() => {
  const old = document.getElementById('__rigd_hl');
  if (old) old.remove();
  return true;
})()`
