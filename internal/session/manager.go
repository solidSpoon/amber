// Package session 是调度器：把「一台一次性测试机」做成可创建、可操作、可回收的会话。
//
// 状态机（只有调度器能改，HTTP/MCP 都通过它）：
//
//	starting ──► ready ──► stopping ──► finished
//	    │          │            │
//	    └► failed  └► abandoned ◄┘
//
// 关键约束：
//   - 容器生命周期只由本包管理，API 层不直接碰 docker；
//   - 每次工具调用都写一条审计事件，并算出它在录像里的偏移量；
//   - 超时回收必须走正常收尾（停录屏、落产物），不能直接删容器。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/solidSpoon/rigd/internal/cdp"
	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// Inspector 是本包用到的检查器能力（便于测试注入假实现）。
type Inspector interface {
	Snapshot(ctx context.Context) (inspect.Snapshot, error)
	Click(ctx context.Context, ref string) error
	ClickAt(ctx context.Context, x, y float64) error
	Fill(ctx context.Context, ref, text string) error
	Press(ctx context.Context, key string) error
	Evaluate(ctx context.Context, script string) (json.RawMessage, error)
	Screenshot(ctx context.Context, path string) (int64, error)
	Close() error
}

// Factory 按调试端口创建检查器。
type Factory func(ctx context.Context, port int) (Inspector, error)

// Manager 会话调度器。并发安全。
type Manager struct {
	cfg    *config.Config
	store  *store.Store
	docker dockerx.Runner
	factory Factory
	log    *slog.Logger

	mu   sync.Mutex
	live map[string]*liveSession
}

// liveSession 只存在于内存的运行时状态。
type liveSession struct {
	session store.Session
	// inspector 连接在容器就绪后建立；建立失败时为 nil，下次取用时重连。
	inspector Inspector
	// recordingStartMs 录像起点（容器内 .recording.json 的墙钟毫秒）。
	recordingStartMs int64
	lastActivity     time.Time
}

// Options 创建会话的输入。
type Options struct {
	// App 应用标识（对应容器镜像里的应用适配脚本）。
	App string
	// Ref 代码版本：分支名或 commit。空表示镜像内置应用。
	Ref string
}

// NewManager 构造调度器。
func NewManager(cfg *config.Config, st *store.Store, docker dockerx.Runner, factory Factory, log *slog.Logger) *Manager {
	if factory == nil {
		factory = func(ctx context.Context, port int) (Inspector, error) {
			return inspect.Dial(ctx, port)
		}
	}
	return &Manager{
		cfg: cfg, store: st, docker: docker, factory: factory, log: log,
		live: map[string]*liveSession{},
	}
}

// ErrTooManySessions 并发会话已满。直接拒绝而不是排队：验收是有时限的人工任务，排队只会让人以为卡住了。
var ErrTooManySessions = errors.New("并发会话已达上限")

// Start 创建会话：分配端口 → 起容器 → 等应用就绪 → 读录像起点 → 置为 ready。
func (m *Manager) Start(ctx context.Context, opts Options) (store.Session, error) {
	m.mu.Lock()
	if len(m.live) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return store.Session{}, ErrTooManySessions
	}
	m.mu.Unlock()

	id := newID()
	dir := filepath.Join(m.cfg.SessionsDir(), id)
	for _, sub := range []string{"video", "screenshots", "report", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return store.Session{}, fmt.Errorf("创建会话目录失败: %w", err)
		}
	}

	cdpPort, err := pickFreePort(m.cfg.CDPPortBase)
	if err != nil {
		return store.Session{}, err
	}
	vncPort, err := pickFreePort(m.cfg.VNCPortBase)
	if err != nil {
		return store.Session{}, err
	}

	sess := store.Session{
		ID:        id,
		App:       opts.App,
		Ref:       opts.Ref,
		Image:     m.cfg.Image,
		Status:    store.StatusStarting,
		CDPPort:   cdpPort,
		VNCPort:   vncPort,
		Dir:       dir,
		CreatedAt: time.Now(),
	}
	if err := m.store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, err
	}

	containerName := "rigd-" + id
	spec := dockerx.Spec{
		Name:        containerName,
		Image:       m.cfg.Image,
		Cmd:         []string{"rig-serve"},
		NetworkHost: true,
		ShmSize:     "512m",
		Labels:      dockerx.DefaultLabels(id),
		Env: map[string]string{
			"RIG_OUT":        "/out",
			"RIG_W":          fmt.Sprint(m.cfg.Display.Width),
			"RIG_H":          fmt.Sprint(m.cfg.Display.Height),
			"RIG_FPS":        fmt.Sprint(m.cfg.Record.FPS),
			"RIG_DEBUG_PORT": fmt.Sprint(cdpPort),
			"RIG_VNC_PORT":   fmt.Sprint(vncPort),
			"RIG_APP_NAME":   opts.App,
			"RIG_GIT_REF":    opts.Ref,
		},
		Mounts: []dockerx.Mount{{HostPath: dir, ContainerPath: "/out"}},
	}

	containerID, err := m.docker.Run(ctx, spec)
	if err != nil {
		sess.Status = store.StatusFailed
		sess.Error = err.Error()
		sess.EndedAt = time.Now()
		_ = m.store.UpdateSession(ctx, sess)
		return sess, fmt.Errorf("启动容器失败: %w", err)
	}

	sess.Container = containerID

	// 就绪 = 调试端口真的能连上。容器「起来了」不等于应用「能操作」：
	// 构建失败、应用崩溃、端口没开，都会让容器活着但永远连不上。
	// 必须等真连上才置 ready，否则调用方会对着一个空壳发工具调用。
	inspector, readyErr := m.factory(ctx, cdpPort)
	if readyErr != nil {
		sess.Status = store.StatusFailed
		sess.Error = fmt.Sprintf("等待应用就绪失败: %v", readyErr)
		sess.EndedAt = time.Now()
		_ = m.store.UpdateSession(ctx, sess)
		if err := m.docker.Stop(ctx, containerName, 10*time.Second); err != nil {
			m.log.Warn("停掉未就绪的容器失败", "session", id, "err", err)
		}
		m.log.Error("会话启动失败", "session", id, "err", readyErr)
		return sess, readyErr
	}

	sess.Status = store.StatusReady
	sess.StartedAt = time.Now()
	sess.IdleDeadline = sess.StartedAt.Add(m.cfg.IdleTimeout)
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		return sess, err
	}

	live := &liveSession{session: sess, inspector: inspector, lastActivity: sess.StartedAt}
	m.mu.Lock()
	m.live[id] = live
	m.mu.Unlock()

	// 录像起点只能在容器内读到；读不到就退化（偏移量为 -1），但要留下告警
	if ms, err := m.readRecordingStart(ctx, containerName); err == nil {
		live.recordingStartMs = ms
	} else {
		m.log.Warn("读取录像起点失败，操作偏移量将退化", "session", id, "err", err)
	}

	m.log.Info("会话就绪", "session", id, "cdp", cdpPort, "vnc", vncPort, "container", containerName)
	return sess, nil
}

// Stop 收尾会话：停容器（让容器内录屏正常收尾）→ 归档产物 → 写结论。
//
// verdict/summary 由调用方给（外部 AI 的结论）；为空时状态仍是 finished，只是没有判定。
func (m *Manager) Stop(ctx context.Context, id, verdict, summary string) (store.Session, error) {
	m.mu.Lock()
	live, ok := m.live[id]
	m.mu.Unlock()
	if !ok {
		// 不在内存里：可能已被超时回收或进程重启，回落到库里的记录
		sess, err := m.store.GetSession(ctx, id)
		if err != nil {
			return store.Session{}, err
		}
		if sess.Status.Terminal() {
			return sess, nil
		}
		return m.finalize(ctx, sess, verdict, summary)
	}

	sess := live.session
	sess.Status = store.StatusStopping
	_ = m.store.UpdateSession(ctx, sess)

	if err := m.docker.Stop(ctx, "rigd-"+id, 25*time.Second); err != nil && !errors.Is(err, dockerx.ErrContainerNotFound) {
		m.log.Warn("停止容器失败", "session", id, "err", err)
	}
	if live.inspector != nil {
		_ = live.inspector.Close()
	}

	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()

	return m.finalize(ctx, sess, verdict, summary)
}

// finalize 收尾的公共部分：读录像元信息、登记产物、落库。
func (m *Manager) finalize(ctx context.Context, sess store.Session, verdict, summary string) (store.Session, error) {
	sess.Status = store.StatusFinished
	sess.EndedAt = time.Now()
	if verdict != "" {
		sess.Verdict = verdict
	}
	if summary != "" {
		sess.Summary = summary
	}

	videoPath := filepath.Join(sess.Dir, "video", "session.mp4")
	if info, err := os.Stat(videoPath); err == nil {
		sess.VideoPath = videoPath
		_ = m.store.AddArtifact(ctx, store.Artifact{
			SessionID: sess.ID, Kind: "video", Path: videoPath, Bytes: info.Size(), CreatedAt: time.Now(),
		})
		if meta, err := readVideoMeta(filepath.Join(sess.Dir, "video", "session.json")); err == nil {
			sess.VideoDurationMS = meta.DurationMS
		}
	}
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		return sess, err
	}
	m.log.Info("会话收尾完成", "session", sess.ID, "verdict", sess.Verdict, "duration_ms", sess.VideoDurationMS)
	return sess, nil
}

// Get 取会话（内存里有就用最新的）。
func (m *Manager) Get(ctx context.Context, id string) (store.Session, error) {
	m.mu.Lock()
	if live, ok := m.live[id]; ok {
		sess := live.session
		m.mu.Unlock()
		return sess, nil
	}
	m.mu.Unlock()
	return m.store.GetSession(ctx, id)
}

// List 列出会话（倒序）。
func (m *Manager) List(ctx context.Context, limit int) ([]store.Session, error) {
	return m.store.ListSessions(ctx, limit)
}

// Events 取会话的审计事件。
func (m *Manager) Events(ctx context.Context, id string) ([]store.Event, error) {
	return m.store.ListEvents(ctx, id)
}

// Touch 记录一次活动，顺延空闲回收时间点。
func (m *Manager) Touch(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if live, ok := m.live[id]; ok {
		live.lastActivity = time.Now()
	}
}

// inspector 取（必要时重建）检查器。
func (m *Manager) inspector(ctx context.Context, id string) (Inspector, error) {
	m.mu.Lock()
	live, ok := m.live[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("会话 %s 不在运行中", id)
	}
	if live.inspector != nil {
		return live.inspector, nil
	}
	insp, err := m.factory(ctx, live.session.CDPPort)
	if err != nil {
		return nil, fmt.Errorf("连接调试端口失败: %w", err)
	}
	m.mu.Lock()
	live.inspector = insp
	m.mu.Unlock()
	return insp, nil
}

// Record 执行一次工具调用并写审计：参数、结果摘要、耗时、在录像里的偏移量。
//
// 所有对外动作都必须走这里，否则审计会出现空洞——那正是这个项目最不能接受的。
func (m *Manager) Record(ctx context.Context, id, tool string, args any, fn func(ctx context.Context, insp Inspector) (result string, artifact string, err error)) (string, string, error) {
	insp, err := m.inspector(ctx, id)
	if err != nil {
		m.writeEvent(ctx, id, tool, args, err.Error(), "", 0, false, -1)
		return "", "", err
	}

	started := time.Now()
	result, artifact, callErr := fn(ctx, insp)
	duration := time.Since(started)

	m.mu.Lock()
	offset := int64(-1)
	if live, ok := m.live[id]; ok {
		live.lastActivity = time.Now()
		if live.recordingStartMs > 0 {
			offset = started.UnixMilli() - live.recordingStartMs
			if offset < 0 {
				offset = 0
			}
		}
	}
	m.mu.Unlock()

	msg := result
	if callErr != nil {
		msg = callErr.Error()
	}
	m.writeEvent(ctx, id, tool, args, msg, artifact, duration.Milliseconds(), callErr == nil, offset)

	if callErr != nil {
		return "", artifact, callErr
	}
	return result, artifact, nil
}

func (m *Manager) writeEvent(ctx context.Context, id, tool string, args any, result, artifact string, durationMS int64, ok bool, offset int64) {
	rawArgs, err := json.Marshal(args)
	if err != nil {
		rawArgs = []byte(`{}`)
	}
	// 审计是旁路：写失败只告警，不影响工具本身的成败
	if _, err := m.store.AppendEvent(ctx, store.Event{
		SessionID: id, At: time.Now(), Tool: tool, Args: rawArgs, OK: ok,
		DurationMS: durationMS, VideoOffsetMS: offset, Result: truncate(result, 2000), ArtifactPath: artifact,
	}); err != nil {
		m.log.Error("写审计事件失败", "session", id, "tool", tool, "err", err)
	}
}

// Sweep 回收超时会话。返回被回收的会话 ID。
//
// 超时不是直接删容器：先走正常收尾（停录屏、归档录像），再标记 abandoned。
func (m *Manager) Sweep(ctx context.Context) []string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.live))
	for id, live := range m.live {
		idleExpired := !live.session.IdleDeadline.IsZero() && time.Now().After(live.session.IdleDeadline)
		lifetimeExpired := time.Since(live.session.StartedAt) > m.cfg.MaxLifetime
		idleByActivity := time.Since(live.lastActivity) > m.cfg.IdleTimeout
		if idleExpired || lifetimeExpired || idleByActivity {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	reclaimed := make([]string, 0, len(ids))
	for _, id := range ids {
		m.log.Warn("会话超时，开始回收", "session", id)
		if _, err := m.Stop(ctx, id, "", "会话超时被回收（可能客户端已断开）"); err != nil {
			m.log.Error("回收会话失败", "session", id, "err", err)
			continue
		}
		sess, err := m.store.GetSession(ctx, id)
		if err == nil {
			sess.Status = store.StatusAbandoned
			_ = m.store.UpdateSession(ctx, sess)
		}
		reclaimed = append(reclaimed, id)
	}
	return reclaimed
}

// RunSweeper 周期性回收超时会话，直到 ctx 取消。
func (m *Manager) RunSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep(ctx)
		}
	}
}

// readRecordingStart 从容器里读录像开始的墙钟毫秒。
//
// 偏移量必须基于录像真实起点，而不是「会话创建时间」：容器要装依赖、构建、启动应用，
// 那几分钟并没有录进视频里，用创建时间会让所有偏移量整体前移。
func (m *Manager) readRecordingStart(ctx context.Context, container string) (int64, error) {
	out, err := m.docker.Exec(ctx, container, "cat", "/out/video/.recording.json")
	if err != nil {
		return 0, err
	}
	var meta struct {
		StartedAtMs int64 `json:"startedAtMs"`
	}
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		return 0, fmt.Errorf("解析录像起点失败: %w", err)
	}
	if meta.StartedAtMs == 0 {
		return 0, errors.New("录像起点为 0，容器可能还没开始录")
	}
	return meta.StartedAtMs, nil
}

type videoMeta struct {
	DurationMS int64 `json:"durationMs"`
	Duration   float64 `json:"duration"`
}

func readVideoMeta(path string) (videoMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return videoMeta{}, err
	}
	var meta videoMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return videoMeta{}, err
	}
	if meta.DurationMS == 0 && meta.Duration > 0 {
		meta.DurationMS = int64(meta.Duration * 1000)
	}
	return meta, nil
}

// newID 生成会话 ID：时间前缀便于粗排，随机后缀避免并发撞车。
func newID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(buf))
}

// pickFreePort 从 base 起找一个当前空闲的端口（同机并行会话靠这个错开）。
func pickFreePort(base int) (int, error) {
	for port := base; port < base+200; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, fmt.Errorf("从 %d 起找不到空闲端口", base)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(截断)"
}

// 让 cdp 包的类型在文档层面可见（inspect 依赖它），避免 import 被误删。
var _ = cdp.Target{}
