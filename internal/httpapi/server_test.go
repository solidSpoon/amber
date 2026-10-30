// Package httpapi 的接口测试：用真实 store + 真实 Manager（假 docker / 假检查器）跑通整套 HTTP 语义。
//
// 这里刻意不 mock 仓储层——异步任务里最容易坏的恰恰是「状态真的落库了没有」，
// 用内存 sqlite 跑真链路，比任何 mock 断言都可靠。
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

type fakeDocker struct{}

func (fakeDocker) Run(context.Context, dockerx.Spec) (string, error) { return "cid", nil }
func (fakeDocker) Stop(context.Context, string, time.Duration) error { return nil }
func (fakeDocker) Inspect(context.Context, string) (dockerx.State, error) {
	return dockerx.State{Running: true}, nil
}
func (fakeDocker) Exec(context.Context, string, ...string) (string, error) {
	return `{"startedAtMs":1700000000000}`, nil
}
func (fakeDocker) CountRunning(context.Context, string) (int, error) { return 1, nil }
func (fakeDocker) Logs(context.Context, string, int) (string, error) { return "app log line\n", nil }
func (fakeDocker) Remove(context.Context, string) error            { return nil }
func (fakeDocker) Stats(context.Context, string) (dockerx.Stats, error) {
	return dockerx.Stats{CPUPct: 12.5, MemUsed: 200 << 20, MemLimit: 2 << 30}, nil
}

type fakeInspector struct {
	mu   sync.Mutex
	shot string
}

func (f *fakeInspector) Snapshot(context.Context) (inspect.Snapshot, error) {
	return inspect.Snapshot{URL: "file:///app/index.html", Title: "t", Text: "hello"}, nil
}
func (f *fakeInspector) Click(context.Context, string) error            { return nil }
func (f *fakeInspector) ClickAt(context.Context, float64, float64) error { return nil }
func (f *fakeInspector) Fill(context.Context, string, string) error     { return nil }
func (f *fakeInspector) Press(context.Context, string) error            { return nil }
func (f *fakeInspector) Evaluate(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"currentTime":5.5}`), nil
}
func (f *fakeInspector) Screenshot(_ context.Context, path string) (int64, error) {
	f.mu.Lock()
	f.shot = path
	f.mu.Unlock()
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		return 0, err
	}
	return 3, nil
}
func (f *fakeInspector) PreviewJPEG(context.Context, int) ([]byte, error) {
	return []byte{0xFF, 0xD8, 0xFF, 0xD9}, nil
}
func (f *fakeInspector) Close() error { return nil }

func newTestServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, Image: "rig-test:latest", MaxSessions: 2,
		IdleTimeout: time.Minute, MaxLifetime: time.Hour,
		CDPPortBase: 29222, VNCPortBase: 26080,
		Display: config.DisplayConfig{Width: 1280, Height: 800},
		Record:  config.RecordConfig{FPS: 20, CRF: 26},
		MediaDir: dir,
	}
	if err := os.MkdirAll(cfg.SessionsDir(), 0o755); err != nil {
		t.Fatalf("创建会话目录失败: %v", err)
	}

	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	insp := &fakeInspector{}
	mgr := session.NewManager(cfg, st, fakeDocker{}, func(context.Context, int) (session.Inspector, error) {
		return insp, nil
	}, log)

	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>app</html>")}}
	srv := httptest.NewServer(New(Options{Config: cfg, Store: st, Manager: mgr, WebFS: webFS, Log: log}).Handler())
	t.Cleanup(srv.Close)
	return srv, mgr
}

func postJSON(t *testing.T, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func getJSON(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// 健康检查是部署探针的契约，必须 200。
func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := getJSON(t, srv.URL+"/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
}

// 完整闭环：创建会话 → 出现在列表里 → 停止 → 状态为 finished。
func TestCreateListStopSession(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, created := postJSON(t, srv.URL+"/api/sessions", map[string]any{"app": "DashPlayer", "ref": "feat/x"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("创建会话期望 202，实际 %d: %v", resp.StatusCode, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("返回里没有 id: %v", created)
	}
	if created["status"] != string(store.StatusStarting) {
		t.Fatalf("刚创建的会话应为 starting，实际 %v", created["status"])
	}
	if created["ref"] != "feat/x" {
		t.Fatalf("ref 未透传: %v", created["ref"])
	}

	_, list := getJSON(t, srv.URL+"/api/sessions")
	sessions, _ := list["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("列表应有 1 个会话，实际 %d", len(sessions))
	}

	resp, stopped := postJSON(t, srv.URL+"/api/sessions/"+id+"/stop", map[string]any{"verdict": "pass", "summary": "ok"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("停止会话期望 200，实际 %d", resp.StatusCode)
	}
	if stopped["status"] != string(store.StatusFinished) || stopped["verdict"] != "pass" {
		t.Fatalf("收尾结果不对: %v", stopped)
	}
}

// 缺 app 必须 400：不猜默认值，避免悄悄起错应用。
func TestCreateSessionRequiresApp(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := postJSON(t, srv.URL+"/api/sessions", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Fatalf("错误响应应带 error 字段: %v", body)
	}
}

// 超出并发上限返回 429，让调用方知道该稍后重试而不是以为坏了。
func TestCreateSessionTooMany(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < 2; i++ {
		if resp, body := postJSON(t, srv.URL+"/api/sessions", map[string]any{"app": "sample"}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("第 %d 个会话应成功: %v", i+1, body)
		}
	}
	resp, body := postJSON(t, srv.URL+"/api/sessions", map[string]any{"app": "sample"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("期望 429，实际 %d: %v", resp.StatusCode, body)
	}
}

// 会话不存在时返回 404，而不是空对象。
func TestGetUnknownSession(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := getJSON(t, srv.URL+"/api/sessions/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", resp.StatusCode)
	}
}

// 单页应用刷新任意路由都要回 index.html，否则管理台刷新就白屏。
func TestSPAFallback(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/abc")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app") {
		t.Fatalf("SPA 回退失败: %d %q", resp.StatusCode, string(body))
	}
}

// 未知 API 路径要 404 JSON，不能被 SPA 回退吞掉。
func TestUnknownAPIPath(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := getJSON(t, srv.URL+"/api/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", resp.StatusCode)
	}
}
