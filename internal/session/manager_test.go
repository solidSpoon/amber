package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// fakeDocker 记录调度器对容器做了什么，避免测试依赖真实 docker。
type fakeDocker struct {
	mu          sync.Mutex
	runSpecs    []dockerx.Spec
	stopped     []string
	execOutput  string
	runErr      error
	stopErr     error
}

func (f *fakeDocker) Run(_ context.Context, spec dockerx.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return "", f.runErr
	}
	f.runSpecs = append(f.runSpecs, spec)
	return "container-" + spec.Name, nil
}

func (f *fakeDocker) Stop(_ context.Context, name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return f.stopErr
}

func (f *fakeDocker) Inspect(context.Context, string) (dockerx.State, error) {
	return dockerx.State{Running: true}, nil
}

func (f *fakeDocker) Exec(context.Context, string, ...string) (string, error) {
	return f.execOutput, nil
}

func (f *fakeDocker) CountRunning(context.Context, string) (int, error) { return 0, nil }

func (f *fakeDocker) Logs(context.Context, string, int) (string, error) { return "", nil }

// fakeInspector 模拟页面：快照固定返回两个元素，点击/输入只记录调用。
type fakeInspector struct {
	clicked []string
	filled  map[string]string
	pressed []string
	shotAt  string
}

func (f *fakeInspector) Snapshot(context.Context) (inspect.Snapshot, error) {
	return inspect.Snapshot{
		URL: "file:///app/index.html", Title: "fake",
		Elements: []inspect.Element{
			{Ref: "e1", Role: "button", Name: "Count", Box: [4]int{10, 10, 80, 30}},
			{Ref: "e2", Role: "textbox", Name: "echo", Box: [4]int{100, 10, 200, 30}},
		},
		Text: "Count: 0",
	}, nil
}

func (f *fakeInspector) Click(_ context.Context, ref string) error {
	f.clicked = append(f.clicked, ref)
	return nil
}

func (f *fakeInspector) ClickAt(context.Context, float64, float64) error { return nil }

func (f *fakeInspector) Fill(_ context.Context, ref, text string) error {
	if f.filled == nil {
		f.filled = map[string]string{}
	}
	f.filled[ref] = text
	return nil
}

func (f *fakeInspector) Press(_ context.Context, key string) error {
	f.pressed = append(f.pressed, key)
	return nil
}

func (f *fakeInspector) Evaluate(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func (f *fakeInspector) Screenshot(_ context.Context, path string) (int64, error) {
	f.shotAt = path
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		return 0, err
	}
	return 3, nil
}

func (f *fakeInspector) Close() error { return nil }

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Addr: "127.0.0.1:0", DataDir: dir, Image: "rig-test:latest", DockerBin: "docker",
		MaxSessions: 2, IdleTimeout: time.Minute, MaxLifetime: time.Hour,
		CDPPortBase: 19222, VNCPortBase: 16080,
		Display: config.DisplayConfig{Width: 1280, Height: 800},
		Record:  config.RecordConfig{FPS: 20, CRF: 26},
		// 测试里给一个存在的目录，模拟已配置的验收素材
		MediaDir: dir,
	}
	if err := os.MkdirAll(cfg.SessionsDir(), 0o755); err != nil {
		t.Fatalf("创建会话根目录失败: %v", err)
	}
	return cfg
}

func newTestManager(t *testing.T) (*Manager, *store.Store, *fakeDocker, *fakeInspector) {
	t.Helper()
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fake := &fakeDocker{execOutput: `{"startedAtMs":1700000000000}`}
	insp := &fakeInspector{}
	factory := func(context.Context, int) (Inspector, error) { return insp, nil }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(cfg, st, fake, factory, log), st, fake, insp
}

// 起会话后：容器收到正确的命令与环境变量、状态为 ready、会话与容器名成对。
func TestStartLaunchesContainerAndBecomesReady(t *testing.T) {
	ctx := context.Background()
	m, st, fake, _ := newTestManager(t)

	sess, err := m.Start(ctx, Options{App: "sample", Ref: ""})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	if sess.Status != store.StatusReady {
		t.Fatalf("状态应为 ready，实际 %s", sess.Status)
	}
	if len(fake.runSpecs) != 1 {
		t.Fatalf("应启动 1 个容器，实际 %d", len(fake.runSpecs))
	}
	spec := fake.runSpecs[0]
	if spec.Name != "rigd-"+sess.ID {
		t.Fatalf("容器名不对: %s", spec.Name)
	}
	if !spec.NetworkHost {
		t.Fatal("必须用宿主网络，否则 Go 侧连不到容器里的 CDP 端口")
	}
	if len(spec.Cmd) != 1 || spec.Cmd[0] != "rig-serve" {
		t.Fatalf("容器命令应为 rig-serve，实际 %v", spec.Cmd)
	}
	if spec.Env["RIG_DEBUG_PORT"] != itoa(sess.CDPPort) {
		t.Fatalf("调试端口未注入: %v", spec.Env)
	}
	// /out 必须有；配置了素材目录时会多挂一个只读的 /assets
	if len(spec.Mounts) == 0 || spec.Mounts[0].ContainerPath != "/out" {
		t.Fatalf("必须挂载 /out: %+v", spec.Mounts)
	}

	// 会话目录与录像目录要提前建好，容器才有地方写
	if _, err := os.Stat(filepath.Join(sess.Dir, "video")); err != nil {
		t.Fatalf("录像目录未创建: %v", err)
	}
	stored, err := st.GetSession(ctx, sess.ID)
	if err != nil || stored.Status != store.StatusReady {
		t.Fatalf("库里的会话状态不对: %+v err=%v", stored, err)
	}
}

// 并发上限必须真的拦住，且不浪费端口/目录。
func TestStartRejectsBeyondMaxSessions(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := newTestManager(t)

	if _, err := m.Start(ctx, Options{App: "sample"}); err != nil {
		t.Fatalf("第 1 个会话应成功: %v", err)
	}
	if _, err := m.Start(ctx, Options{App: "sample"}); err != nil {
		t.Fatalf("第 2 个会话应成功: %v", err)
	}
	if _, err := m.Start(ctx, Options{App: "sample"}); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("第 3 个会话应被拒绝，实际 %v", err)
	}
}

// 工具调用要落审计，并且偏移量按「录像起点」算，而不是会话创建时间。
func TestRecordWritesAuditWithVideoOffset(t *testing.T) {
	ctx := context.Background()
	m, st, _, insp := newTestManager(t)

	sess, err := m.Start(ctx, Options{App: "sample", Ref: ""})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	// 把录像起点设成「现在」，模拟容器刚开始录
	m.mu.Lock()
	m.live[sess.ID].recordingStartMs = time.Now().UnixMilli()
	m.mu.Unlock()

	if _, _, err := m.Record(ctx, sess.ID, "click", map[string]any{"ref": "e1"},
		func(ctx context.Context, i Inspector) (string, string, error) {
			return "clicked e1", "", i.Click(ctx, "e1")
		}); err != nil {
		t.Fatalf("记录点击失败: %v", err)
	}
	if len(insp.clicked) != 1 || insp.clicked[0] != "e1" {
		t.Fatalf("点击没有真正执行: %v", insp.clicked)
	}

	events, err := st.ListEvents(ctx, sess.ID)
	if err != nil {
		t.Fatalf("读事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("应有 1 条事件，实际 %d", len(events))
	}
	if events[0].Tool != "click" || !events[0].OK {
		t.Fatalf("事件内容不对: %+v", events[0])
	}
	if events[0].VideoOffsetMS < 0 {
		t.Fatalf("录像偏移量不应为负: %d", events[0].VideoOffsetMS)
	}
}

// 工具失败也要留痕：审计不能只记录成功的动作。
func TestRecordLogsFailedToolCall(t *testing.T) {
	ctx := context.Background()
	m, st, _, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{App: "DashPlayer"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}

	_, _, callErr := m.Record(ctx, sess.ID, "click", map[string]any{"ref": "e404"},
		func(context.Context, Inspector) (string, string, error) {
			return "", "", errors.New("元素引用已失效")
		})
	if callErr == nil {
		t.Fatal("错误应被透传")
	}

	events, _ := st.ListEvents(ctx, sess.ID)
	if len(events) != 1 || events[0].OK {
		t.Fatalf("失败调用也应落审计且 ok=false: %+v", events)
	}
	if events[0].Result == "" {
		t.Fatal("失败原因应写进结果字段")
	}
}

// 收尾：停容器、登记录像产物、状态置为 finished。
func TestStopFinalizesSessionAndRegistersVideo(t *testing.T) {
	ctx := context.Background()
	m, st, fake, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{App: "DashPlayer"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}

	// 模拟容器收尾时写出的录像与元信息
	videoPath := filepath.Join(sess.Dir, "video", "session.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4"), 0o644); err != nil {
		t.Fatalf("写录像失败: %v", err)
	}
	meta := `{"duration": 42.5, "frames": 850}`
	if err := os.WriteFile(filepath.Join(sess.Dir, "video", "session.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("写元信息失败: %v", err)
	}

	done, err := m.Stop(ctx, sess.ID, "pass", "字幕与画面一致")
	if err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	if done.Status != store.StatusFinished || done.Verdict != "pass" {
		t.Fatalf("收尾结果不对: %+v", done)
	}
	if done.VideoDurationMS != 42500 {
		t.Fatalf("录像时长应为 42500ms，实际 %d", done.VideoDurationMS)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "rigd-"+sess.ID {
		t.Fatalf("应停止容器: %v", fake.stopped)
	}

	artifacts, err := st.ListArtifacts(ctx, sess.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Kind != "video" {
		t.Fatalf("录像未登记为产物: %+v err=%v", artifacts, err)
	}

	stored, _ := st.GetSession(ctx, sess.ID)
	if stored.Status != store.StatusFinished {
		t.Fatalf("库里的状态未更新: %s", stored.Status)
	}
}

// 超时会话要被回收成 abandoned，且走的是正常收尾（停容器、留录像），不是直接删。
func TestSweepReclaimsIdleSession(t *testing.T) {
	ctx := context.Background()
	m, st, fake, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{App: "DashPlayer"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}

	// 把空闲截止时间与最后活动都拨到过去
	m.mu.Lock()
	m.live[sess.ID].session.IdleDeadline = time.Now().Add(-time.Minute)
	m.live[sess.ID].lastActivity = time.Now().Add(-time.Hour)
	m.mu.Unlock()

	reclaimed := m.Sweep(ctx)
	if len(reclaimed) != 1 || reclaimed[0] != sess.ID {
		t.Fatalf("应回收 1 个会话: %v", reclaimed)
	}
	if len(fake.stopped) != 1 {
		t.Fatalf("回收时必须正常停容器: %v", fake.stopped)
	}
	stored, _ := st.GetSession(ctx, sess.ID)
	if stored.Status != store.StatusAbandoned {
		t.Fatalf("状态应为 abandoned，实际 %s", stored.Status)
	}
	if stored.EndedAt.IsZero() {
		t.Fatal("回收也要写结束时间")
	}
}

// 已终态的会话再次收尾应当幂等（客户端重试 / 重复调用很常见）。
func TestStopIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{App: "DashPlayer"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	if _, err := m.Stop(ctx, sess.ID, "pass", "ok"); err != nil {
		t.Fatalf("第一次收尾失败: %v", err)
	}
	if _, err := m.Stop(ctx, sess.ID, "", ""); err != nil {
		t.Fatalf("重复收尾应成功: %v", err)
	}
}

func itoa(n int) string {
	return json.Number(json.Number(fmtInt(n))).String()
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// 应用注册表决定容器里拿到哪些环境变量；配错了容器会起不来，所以单独钉一条。
func TestStartInjectsAppContract(t *testing.T) {
	ctx := context.Background()
	m, _, fake, _ := newTestManager(t)

	if _, err := m.Start(ctx, Options{App: "DashPlayer", Ref: "feat/x"}); err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	env := fake.runSpecs[0].Env
	want := map[string]string{
		"RIG_GIT_URL":      "https://github.com/solidSpoon/DashPlayer.git",
		"RIG_GIT_REF":      "feat/x",
		"RIG_APP_DIR":      "/app",
		"RIG_BUILD_SCRIPT": "/opt/apps/dashplayer/build.sh",
		"RIG_BOOTSTRAP":    "/opt/apps/dashplayer/bootstrap.mjs",
		"RIG_APP_NAME":     "DashPlayer",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s 期望 %q，实际 %q", key, value, env[key])
		}
	}
}

// 未知应用必须启动前就报错，不能悄悄起一个空容器。
func TestStartRejectsUnknownApp(t *testing.T) {
	ctx := context.Background()
	m, _, fake, _ := newTestManager(t)
	if _, err := m.Start(ctx, Options{App: "不存在的应用"}); err == nil {
		t.Fatal("未知应用应报错")
	}
	if len(fake.runSpecs) != 0 {
		t.Fatalf("未知应用不应启动容器: %v", fake.runSpecs)
	}
}
