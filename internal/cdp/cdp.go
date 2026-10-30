// Package cdp 是一个最小的 Chrome DevTools Protocol 客户端（websocket + JSON-RPC）。
//
// 为什么不用 chromedp：验收只需要四件事——执行 JS、截一张图、发鼠标/键盘事件、读目标列表。
// 裸协议不到 200 行，行为完全可预期；而 Electron 的 CDP 版本跟着 Chromium 走，
// 少一层封装就少一处「升级后行为变了」的惊喜。
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Target 一个可调试目标（Electron 的渲染进程就是一个 page target）。
type Target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Client 绑定到某一个 page target 的连接。
type Client struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	nextID int64
	pending map[int64]chan rawResponse
	closed chan struct{}
	once   sync.Once
}

type rawResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ListTargets 读 /json/list，拿到该端口上所有可调试目标。
func ListTargets(ctx context.Context, port int, timeout time.Duration) ([]Target, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 CDP 目标列表失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDP 目标列表返回 %d", resp.StatusCode)
	}

	var targets []Target
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("解析目标列表失败: %w", err)
	}
	return targets, nil
}

// DialPage 连到第一个 page 类型的目标（Electron 的主窗口）。
//
// Electron 启动后先有浏览器级端点、后有渲染进程，所以这里用轮询等页面出现，
// 而不是假设“端口通就等于能操作”。
func DialPage(ctx context.Context, port int, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		targets, err := ListTargets(ctx, port, 5*time.Second)
		if err == nil {
			for _, t := range targets {
				if t.Type != "page" || t.WebSocketDebuggerURL == "" {
					continue
				}
				client, err := dial(ctx, t.WebSocketDebuggerURL)
				if err != nil {
					lastErr = err
					continue
				}
				return client, nil
			}
			lastErr = fmt.Errorf("端口 %d 上没有 page 目标", port)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("等待 CDP 页面超时(%s): %w", timeout, lastErr)
}

func dial(ctx context.Context, wsURL string) (*Client, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 CDP websocket 失败: %w", err)
	}
	c := &Client{
		conn:    conn,
		pending: make(map[int64]chan rawResponse),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop 把响应按 id 分发给等待者；事件（无 id）直接丢弃。
func (c *Client) readLoop() {
	defer c.shutdown()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var probe struct {
			ID *int64 `json:"id"`
		}
		if err := json.Unmarshal(data, &probe); err != nil || probe.ID == nil {
			continue // 事件帧，暂不消费
		}
		var resp rawResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
			close(ch)
		}
	}
}

// Call 发一个 CDP 命令并等结果。params 可以为 nil。
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rawResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 CDP 请求失败: %w", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, body); err != nil {
		return fmt.Errorf("发送 CDP 请求失败: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("CDP 连接已关闭")
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("CDP %s 失败: %s", method, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("解析 CDP 结果失败: %w", err)
			}
		}
		return nil
	}
}

// Evaluate 在页面里执行表达式，返回 result.value 的原始 JSON。
func (c *Client) Evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	params := map[string]any{
		"expression":   expression,
		"awaitPromise": true,
		"returnByValue": true,
	}
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.Call(ctx, "Runtime.evaluate", params, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		return nil, fmt.Errorf("页面脚本异常: %s", out.ExceptionDetails.Text)
	}
	return out.Result.Value, nil
}

// Screenshot 截当前页面（PNG base64）。
func (c *Client) Screenshot(ctx context.Context) (string, error) {
	return c.capture(ctx, "png", 0)
}

// ScreenshotJPEG 截 JPEG（看板实时预览用：同样画面体积只有 PNG 的 1/5）。
//
// quality 取 1~100，0 表示用 CDP 默认值。
func (c *Client) ScreenshotJPEG(ctx context.Context, quality int) (string, error) {
	return c.capture(ctx, "jpeg", quality)
}

func (c *Client) capture(ctx context.Context, format string, quality int) (string, error) {
	params := map[string]any{"format": format}
	if quality > 0 {
		params["quality"] = quality
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := c.Call(ctx, "Page.captureScreenshot", params, &out); err != nil {
		return "", err
	}
	return out.Data, nil
}

// ClickAt 在页面坐标 (x, y) 上模拟一次真实鼠标点击。
//
// 用坐标而不是直接 DOM.click()：前者会经过命中测试与事件冒泡，跟真人点击一致，
// 能暴露「元素被遮挡」「坐标算错」这类问题。
func (c *Client) ClickAt(ctx context.Context, x, y float64) error {
	base := map[string]any{"x": x, "y": y, "button": "left", "clickCount": 1}
	if err := c.Call(ctx, "Input.dispatchMouseEvent", merge(base, "mousePressed"), nil); err != nil {
		return err
	}
	return c.Call(ctx, "Input.dispatchMouseEvent", merge(base, "mouseReleased"), nil)
}

// TypeText 输入文本（逐字符派发 keyDown/char/keyUp 的简化版：用 insertText）。
func (c *Client) TypeText(ctx context.Context, text string) error {
	return c.Call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}

// KeyPress 发一次按键（keys 用 CDP 的 key 描述，如 "Enter"、"Space"、"ArrowRight"）。
func (c *Client) KeyPress(ctx context.Context, key string) error {
	down := map[string]any{
		"type": "keyDown", "key": key, "windowsVirtualKeyCode": virtualKeyCode(key),
		"nativeVirtualKeyCode": virtualKeyCode(key),
	}
	if err := c.Call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return c.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": key, "windowsVirtualKeyCode": virtualKeyCode(key),
		"nativeVirtualKeyCode": virtualKeyCode(key),
	}, nil)
}

// Close 关闭连接（幂等）。
func (c *Client) Close() error {
	var err error
	c.once.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

func (c *Client) shutdown() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

func merge(base map[string]any, eventType string) map[string]any {
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["type"] = eventType
	return out
}

// virtualKeyCode 给常见按键补上虚拟键码，Electron 的快捷键处理依赖它。
func virtualKeyCode(key string) int {
	switch key {
	case "Enter":
		return 13
	case "Escape":
		return 27
	case "Space", " ":
		return 32
	case "ArrowLeft":
		return 37
	case "ArrowUp":
		return 38
	case "ArrowRight":
		return 39
	case "ArrowDown":
		return 40
	case "Tab":
		return 9
	case "Backspace":
		return 8
	}
	if len(key) == 1 {
		return int(key[0])
	}
	return 0
}
