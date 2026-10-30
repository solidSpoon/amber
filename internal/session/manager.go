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
	"strings"
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
	// PreviewJPEG 取一帧 JPEG（看板实时预览），不落盘
	PreviewJPEG(ctx context.Context, quality int) ([]byte, error)
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

	statsMu    sync.Mutex
	statsCache map[string]statsEntry
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

// Start 同步完成准备与启动，返回就绪（或失败）的会话。
//
// MCP 场景适合用它：工具调用本来就是「等待结果」的语义。
// HTTP 场景用 Prepare + Provision，避免一个请求挂几分钟。
func (m *Manager) Start(ctx context.Context, opts Options) (store.Session, error) {
	sess, err := m.Prepare(ctx, opts)
	if err != nil {
		return store.Session{}, err
	}
	return m.Provision(ctx, sess)
}

// Prepare 只做同步的登记工作：校验应用、占端口、建目录、写库（status=starting）。
//
// 拆出来是为了让 HTTP 能立刻返回会话 ID——构建镜像/启动应用要几分钟，
// 让请求挂在那儿既过不了网关超时，也没法提前把进度推给前端。
func (m *Manager) Prepare(ctx context.Context, opts Options) (store.Session, error) {
	m.mu.Lock()
	if len(m.live) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return store.Session{}, ErrTooManySessions
	}
	m.mu.Unlock()

	spec, err := LookupApp(opts.App)
	if err != nil {
		return store.Session{}, err
	}
	// 素材缺失要在起容器之前就失败，别等到构建脚本 cp 才报 ENOENT
	if spec.RequiresMedia && m.cfg.MediaDir == "" {
		return store.Session{}, fmt.Errorf("应用 %s 需要验收素材，但未配置 RIGD_MEDIA_DIR", opts.App)
	}

	// 槽位 = 看板上的机器位置，固定 0..MaxSessions-1；占满则拒绝
	used, err := m.store.UsedSlots(ctx)
	if err != nil {
		return store.Session{}, err
	}
	slot := -1
	for i := 0; i < m.cfg.MaxSessions; i++ {
		if _, taken := used[i]; !taken {
			slot = i
			break
		}
	}
	if slot < 0 {
		return store.Session{}, ErrTooManySessions
	}

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
		App:       opts.App, // 存注册表 key（稳定标识）；展示名用 appSpec.Name
		Ref:       opts.Ref,
		Image:     m.cfg.Image,
		Status:    store.StatusStarting,
		CDPPort:   cdpPort,
		VNCPort:   vncPort,
		Slot:      slot,
		Dir:       dir,
		CreatedAt: time.Now(),
	}
	if err := m.store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, err
	}

	return sess, nil
}

// Provision 起容器并等应用就绪：docker run → 等 CDP → 读录像起点 → 置 ready。
//
// 就绪失败会把会话标记 failed 并停掉容器，绝不留下「容器活着但连不上」的空壳。
func (m *Manager) Provision(ctx context.Context, sess store.Session) (store.Session, error) {
	id := sess.ID
	containerName := "rigd-" + id
	cdpPort := sess.CDPPort
	vncPort := sess.VNCPort
	appSpec, err := LookupApp(sess.App)
	if err != nil {
		return sess, err
	}

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
			"RIG_APP_NAME":   appSpec.Name,
			"RIG_APP_DIR":    appSpec.AppDir,
			"RIG_GIT_REF":    sess.Ref,
		},
		Mounts: mountsFor(appSpec, sess.Dir, m.cfg.MediaDir),
	}

	if appSpec.YarnCacheVolume != "" {
		spec.Env["YARN_CACHE_FOLDER"] = "/cache/yarn"
	}
	if appSpec.ElectronCacheVolume != "" {
		spec.Env["RIG_ELECTRON_CACHE"] = "/opt/electron-cache"
	}
	if appSpec.Repo != "" && sess.Ref != "" {
		spec.Env["RIG_GIT_URL"] = appSpec.Repo
	}
	if appSpec.BuildScript != "" {
		spec.Env["RIG_BUILD_SCRIPT"] = appSpec.BuildScript
	}
	if appSpec.Bootstrap != "" {
		spec.Env["RIG_BOOTSTRAP"] = appSpec.Bootstrap
	}
	if appSpec.Repo == "" {
		// 镜像内置应用：容器侧不需要取源码
		spec.Env["RIG_SOURCE_MODE"] = "bundled"
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
		// 容器里为什么没起来，答案只在它的日志里：抓下来写进会话目录，别让它随 --rm 消失
		detail := fmt.Sprintf("等待应用就绪失败: %v", readyErr)
		if logs, err := m.docker.Logs(ctx, containerName, 120); err == nil && strings.TrimSpace(logs) != "" {
			_ = os.WriteFile(filepath.Join(sess.Dir, "logs", "container.log"), []byte(logs), 0o644)
			detail = fmt.Sprintf("%s\n容器日志尾部：\n%s", detail, tailLines(logs, 15))
		}
		sess.Status = store.StatusFailed
		sess.Error = detail
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

	// 记录验的到底是哪个 commit：容器取码时写了 source.json，读不回来就留空，
	// 但绝不能拿「分支名」冒充 sha——报告里那行必须可信。
	if commit, err := m.readCommitSHA(ctx, containerName); err == nil && commit != "" {
		sess.CommitSHA = commit
		_ = m.store.UpdateSession(ctx, sess)
		m.mu.Lock()
		live.session.CommitSHA = commit
		m.mu.Unlock()
	} else if err != nil {
		m.log.Warn("读取 commit 失败", "session", id, "err", err)
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

// readCommitSHA 从容器里读源码元信息（rig-fetch-source 写的 source.json）。
func (m *Manager) readCommitSHA(ctx context.Context, container string) (string, error) {
	out, err := m.docker.Exec(ctx, container, "cat", "/out/logs/source.json")
	if err != nil {
		return "", err
	}
	var meta struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		return "", fmt.Errorf("解析 source.json 失败: %w", err)
	}
	return meta.Commit, nil
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

// tailLines 取文本最后 n 行，用于把容器日志尾部塞进错误信息。
func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// mountsFor 组装挂载：/out 必须有；依赖缓存卷只有应用声明了才挂
// （内置示例应用不需要，挂空卷名会直接让 docker 报错）。
func mountsFor(spec AppSpec, sessionDir, mediaDir string) []dockerx.Mount {
	mounts := []dockerx.Mount{{Source: sessionDir, ContainerPath: "/out"}}
	if mediaDir != "" {
		mounts = append(mounts, dockerx.Mount{Source: mediaDir, ContainerPath: "/assets", ReadOnly: true})
	}
	add := func(volume, target string) {
		if volume == "" {
			return
		}
		mounts = append(mounts, dockerx.Mount{Source: volume, ContainerPath: target})
	}
	add(spec.NodeModulesVolume, filepath.Join(spec.AppDir, "node_modules"))
	add(spec.YarnCacheVolume, "/cache/yarn")
	add(spec.ElectronCacheVolume, "/opt/electron-cache")
	return mounts
}

// MachineView 看板上的一台机器：固定槽位 + 当前占用它的会话（可能没有）。
type MachineView struct {
	Slot      int           `json:"slot"`
	Occupied  bool          `json:"occupied"`
	SessionID string        `json:"sessionId,omitempty"`
	App       string        `json:"app,omitempty"`
	Ref       string        `json:"ref,omitempty"`
	Status    string        `json:"status,omitempty"`
	Since     time.Time     `json:"since,omitempty"`
	IdleUntil time.Time     `json:"idleUntil,omitempty"`
	CDPPort   int           `json:"cdpPort,omitempty"`
	VNCPort   int           `json:"vncPort,omitempty"`
	Stats     dockerx.Stats `json:"stats"`
	StatsOK   bool          `json:"statsOk"`
}

// Machines 返回全部槽位状态（看板首页的数据源）。
//
// 统计数字走 docker stats，单次调用要几十到几百毫秒，所以做 3 秒缓存：
// 看板本来就是"看个大概"，没必要每次刷新都把 docker 敲一遍。
func (m *Manager) Machines(ctx context.Context) ([]MachineView, error) {
	used, err := m.store.UsedSlots(ctx)
	if err != nil {
		return nil, err
	}

	// 用一次查询把活跃会话取出来，避免逐槽位查库
	active, err := m.store.ActiveSessions(ctx)
	if err != nil {
		return nil, err
	}
	bySlot := make(map[int]store.Session, len(active))
	for _, sess := range active {
		bySlot[sess.Slot] = sess
	}

	out := make([]MachineView, 0, m.cfg.MaxSessions)
	for slot := 0; slot < m.cfg.MaxSessions; slot++ {
		view := MachineView{Slot: slot}
		if sessID, ok := used[slot]; ok {
			sess := bySlot[slot]
			view.Occupied = true
			view.SessionID = sessID
			view.App = sess.App
			view.Ref = sess.Ref
			view.Status = string(sess.Status)
			view.Since = sess.StartedAt
			view.IdleUntil = sess.IdleDeadline
			view.CDPPort = sess.CDPPort
			view.VNCPort = sess.VNCPort
			if stats, ok := m.cachedStats(sess.Container); ok {
				view.Stats = stats
				view.StatsOK = true
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// Preview 取会话的一帧预览（不写审计：看板刷屏不应污染验收记录）。
func (m *Manager) Preview(ctx context.Context, id string, quality int) ([]byte, error) {
	insp, err := m.inspector(ctx, id)
	if err != nil {
		return nil, err
	}
	preview, ok := insp.(interface {
		PreviewJPEG(ctx context.Context, quality int) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("当前检查器不支持预览帧")
	}
	return preview.PreviewJPEG(ctx, quality)
}

// cachedStats 3 秒内的容器统计直接复用。
func (m *Manager) cachedStats(container string) (dockerx.Stats, bool) {
	if container == "" {
		return dockerx.Stats{}, false
	}
	m.statsMu.Lock()
	if cached, ok := m.statsCache[container]; ok && time.Since(cached.at) < 3*time.Second {
		m.statsMu.Unlock()
		return cached.stats, true
	}
	m.statsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stats, err := m.docker.Stats(ctx, container)
	if err != nil {
		return dockerx.Stats{}, false
	}
	m.statsMu.Lock()
	if m.statsCache == nil {
		m.statsCache = map[string]statsEntry{}
	}
	m.statsCache[container] = statsEntry{stats: stats, at: time.Now()}
	m.statsMu.Unlock()
	return stats, true
}

type statsEntry struct {
	stats dockerx.Stats
	at    time.Time
}

// RecoverSessions 进程启动时接管仍在运行的会话。
//
// 没有这一步，重启控制面就会"孤儿化"：库里有记录、容器还在跑，但内存里的连接没了，
// 看板画面与工具调用都会失效。这里重建 live 状态，包括录像起点（决定审计偏移）。
func (m *Manager) RecoverSessions(ctx context.Context) (int, error) {
	active, err := m.store.ActiveSessions(ctx)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, sess := range active {
		live := &liveSession{session: sess, lastActivity: time.Now()}

		// 只接管"明确就绪"的会话：starting/stopping 是上一个进程留下的半截状态，
		// 接管它们要么白等端口，要么留下永远收不完的尾。
		if sess.Status != store.StatusReady {
			sess.Status = store.StatusFailed
			sess.Error = fmt.Sprintf("控制面重启时该会话处于 %s 状态，已放弃接管", sess.Status)
			sess.EndedAt = time.Now()
			_ = m.store.UpdateSession(ctx, sess)
			continue
		}

		// 容器没了就标记失败，别让它一直挂在"运行中"
		state, err := m.docker.Inspect(ctx, "rigd-"+sess.ID)
		if err != nil || !state.Running {
			sess.Status = store.StatusFailed
			sess.Error = "控制面重启时容器已不在运行"
			sess.EndedAt = time.Now()
			_ = m.store.UpdateSession(ctx, sess)
			continue
		}

		// 接管必须快：端口连不上就放弃，不能把启动流程堵在这里（原来会阻塞 5 分钟）
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		inspector, dialErr := m.factory(dialCtx, sess.CDPPort)
		cancel()
		if dialErr == nil {
			live.inspector = inspector
		} else {
			m.log.Warn("接管会话时连接调试端口失败，会话标记为失败", "session", sess.ID, "err", dialErr)
			sess.Status = store.StatusFailed
			sess.Error = fmt.Sprintf("接管失败：%v", dialErr)
			sess.EndedAt = time.Now()
			_ = m.store.UpdateSession(ctx, sess)
			continue
		}
		if ms, err := m.readRecordingStart(ctx, "rigd-"+sess.ID); err == nil {
			live.recordingStartMs = ms
		}
		if commit, err := m.readCommitSHA(ctx, "rigd-"+sess.ID); err == nil && commit != "" {
			sess.CommitSHA = commit
			live.session.CommitSHA = commit
			_ = m.store.UpdateSession(ctx, sess)
		}

		m.mu.Lock()
		m.live[sess.ID] = live
		m.mu.Unlock()
		recovered++
	}
	if recovered > 0 {
		m.log.Info("已接管仍在运行的会话", "count", recovered)
	}
	return recovered, nil
}
