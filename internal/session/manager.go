// Package session 是调度器：把「一台一次性机器」做成可创建、可操作、可回收的会话。
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

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// Inspector 是本包用到的检查器能力（便于测试注入假实现）。
type Inspector interface {
	Snapshot(ctx context.Context) (inspect.Snapshot, error)
	Click(ctx context.Context, ref string) error
	Hover(ctx context.Context, ref string) error
	HoverAt(ctx context.Context, x, y float64) error
	ClickAt(ctx context.Context, x, y float64) error
	Fill(ctx context.Context, ref, text string) error
	Press(ctx context.Context, key string) error
	Evaluate(ctx context.Context, script string) (json.RawMessage, error)
	Close() error
}

// Factory 按调试端口创建检查器。
type Factory func(ctx context.Context, port int) (Inspector, error)

// Manager 会话调度器。并发安全。
type Manager struct {
	cfg     *config.Config
	store   *store.Store
	docker  dockerx.Runner
	factory Factory
	log     *slog.Logger

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
	// viewportW/H 上次量到的页面视口尺寸。窗口变化靠它判断：
	// 视口（client area）和窗口矩形（含边框/装饰）不是一回事，不能互相比较。
	viewportW, viewportH int
	lastActivity         time.Time
}

// Options 创建会话的输入。
type Options struct {
	// Label 展示名（一般是产物 rig.json 里的 name）；留空时用产物目录名
	Label string
	// Title 这次验收的意图（一句话）：记录顶部和会话列表先展示它，而不是一串 ID。
	Title string
	// Payload 被测产物：dir:/绝对路径 或 manifest:sha256:…
	Payload string
	// Assets 素材槽位 → 来源（槽位由产物自己的 rig.json 声明）
	Assets map[string]string
	// Commit 可选的来源版本号，只用于记录溯源
	Commit string
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

	if strings.TrimSpace(opts.Payload) == "" {
		return store.Session{}, errors.New("必须指定 payload（dir:/绝对路径 或 manifest:sha256:…）")
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

	// 先把产物落地、读自述、对素材槽位：这些都在起容器之前做完，
	// 配错了立刻失败，而不是让容器跑到一半才发现入口不存在。
	payload, err := m.resolvePayload(ctx, dir, opts.Payload, opts.Assets, true)
	if err != nil {
		_ = os.RemoveAll(dir)
		return store.Session{}, err
	}

	cdpPort, err := pickFreePort(m.cfg.CDPPortBase)
	if err != nil {
		return store.Session{}, err
	}
	vncPort, err := pickFreePort(m.cfg.VNCPortBase)
	if err != nil {
		return store.Session{}, err
	}

	label := strings.TrimSpace(opts.Label)
	if label == "" {
		label = payload.Recipe.Name
	}
	if label == "" {
		label = filepath.Base(payload.Dir)
	}
	assetsJSON, _ := json.Marshal(opts.Assets)

	sess := store.Session{
		ID:        id,
		Title:     strings.TrimSpace(opts.Title),
		App:       label,
		Ref:       opts.Commit,
		Status:    store.StatusStarting,
		CDPPort:   cdpPort,
		VNCPort:   vncPort,
		Slot:      slot,
		Dir:       dir,
		Payload:   opts.Payload,
		Assets:    string(assetsJSON),
		CreatedAt: time.Now(),
	}
	if err := m.store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, err
	}

	// 引用住用到的 manifest（payload + 每个 manifest 素材），回收时才不会被提前删掉
	m.retainSources(ctx, opts.Payload, opts.Assets)
	return sess, nil
}

// eachManifestSource 遍历这次会话引用到的所有 manifest（产物 + 素材）。
//
// 引用和释放必须看到同一份清单：两边各拼一遍，加个新来源就会"只加引用不释放"，
// 表现是产物永远回收不掉。
func (m *Manager) eachManifestSource(payload string, assets map[string]string, fn func(digest string)) {
	sources := make([]string, 0, len(assets)+1)
	sources = append(sources, payload)
	for _, value := range assets {
		sources = append(sources, value)
	}
	seen := make(map[string]bool, len(sources))
	for _, raw := range sources {
		src, err := ParseSource(raw)
		if err != nil || src.Kind != sourceManifest || seen[src.Digest] {
			continue
		}
		seen[src.Digest] = true
		fn(src.Digest)
	}
}

// retainSources 给用到的 manifest 加引用；尽力而为（失败只告警，不影响会话）。
func (m *Manager) retainSources(ctx context.Context, payload string, assets map[string]string) {
	m.eachManifestSource(payload, assets, func(digest string) {
		if err := m.store.Retain(ctx, store.KindManifest, digest); err != nil {
			m.log.Warn("增加产物引用失败", "manifest", digest, "err", err)
		}
	})
}

// releaseSources 归还引用（收尾时调用），让 GC 能把没人用的产物删掉。
func (m *Manager) releaseSources(ctx context.Context, sess store.Session) {
	m.eachManifestSource(sess.Payload, decodeAssets(sess.Assets), func(digest string) {
		if err := m.store.Release(ctx, store.KindManifest, digest); err != nil {
			m.log.Warn("释放产物引用失败", "manifest", digest, "err", err)
		}
	})
}

// liveOf 取内存里的会话状态（没有就是不在运行中）。
func (m *Manager) liveOf(id string) (*liveSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live, ok := m.live[id]
	return live, ok
}

// fail 把会话标记成失败并落库，返回写好错误的那份会话。
//
// 失败路径有五六处（产物对不上、容器起不来、应用没就绪、重启时接管不了……），
// 每处都手写这三个字段，迟早会漏掉一个，于是库里出现了"失败但没有结束时间"的会话。
func (m *Manager) fail(ctx context.Context, sess store.Session, reason string) store.Session {
	sess.Status = store.StatusFailed
	sess.Error = reason
	sess.EndedAt = time.Now()
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		m.log.Warn("写入失败状态失败", "session", sess.ID, "err", err)
	}
	return sess
}

// displayOf 是这台机器在容器里的 X 屏号。
//
// 一槽位一号是硬要求：容器走 --network=host，X 的抽象套接字落在共享网络命名空间里
// （不受 /tmp 隔离），两台机器都用同一个号时，后起的那台会直接
// "server already running" 起不来。号跟槽位绑，重启后同一槽位还是同一个号。
//
// 控制面所有 x11 命令（录屏取景、整屏抓帧、xdotool）都必须走这里，
// 否则抓到的是另一块屏，画面要么全黑要么张冠李戴。
func (m *Manager) displayOf(slot int) int {
	return m.cfg.Display.Num + slot
}

// Provision 起容器并等应用就绪：docker run → 等 CDP → 读录像起点 → 置 ready。
//
// 就绪失败会把会话标记 failed 并停掉容器，绝不留下「容器活着但连不上」的空壳。
func (m *Manager) Provision(ctx context.Context, sess store.Session) (store.Session, error) {
	containerName := containerNameOf(sess.ID)

	payload, err := m.resolvePayload(ctx, sess.Dir, sess.Payload, decodeAssets(sess.Assets), false)
	if err != nil {
		return m.fail(ctx, sess, err.Error()), err
	}

	containerID, err := m.docker.Run(ctx, m.containerSpec(sess, payload, containerName))
	if err != nil {
		return m.fail(ctx, sess, err.Error()), fmt.Errorf("启动容器失败: %w", err)
	}
	sess.Container = containerID

	// 就绪 = 调试端口真的能连上。容器「起来了」不等于应用「能操作」：
	// 构建失败、应用崩溃、端口没开，都会让容器活着但永远连不上。
	// 必须等真连上才置 ready，否则调用方会对着一个空壳发工具调用。
	inspector, readyErr := m.factory(ctx, sess.CDPPort)
	if readyErr != nil {
		return m.abortProvision(ctx, sess, containerName, readyErr), readyErr
	}

	// 取景 = 应用窗口，而取景是在容器里、开录之前定下来的（rig-fit-window）。
	// 这里只读、不动窗口：录像的取景已经固定，现在再改窗口只会让画面和录像对不上。
	// 同时必须等容器把「取景 + 开录 + 应用前置」全做完，否则前端会拿到
	// 还没开始录的会话（偏移量全为 -1）。
	if err := m.waitContainerReady(ctx, containerName); err != nil {
		m.log.Warn("等待容器就绪标记超时，会话仍继续", "session", sess.ID, "err", err)
	}
	sess = m.recordGeometry(ctx, sess, containerName)

	// 录像起点只能在容器内读到。它既是偏移量的原点（内存里用），
	// 也要落库——收尾导出时间线时得带上它，否则 offsetMs 对不上任何墙钟时间。
	recordingStartMS, recErr := m.readRecordingStart(ctx, containerName)
	if recErr != nil {
		m.log.Warn("读取录像起点失败，操作偏移量将退化", "session", sess.ID, "err", recErr)
	}
	sess.RecordingStartMs = recordingStartMS

	sess.Status = store.StatusReady
	sess.StartedAt = time.Now()
	sess.IdleDeadline = sess.StartedAt.Add(m.cfg.IdleTimeout)
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		return sess, err
	}

	live := &liveSession{
		session: sess, inspector: inspector,
		recordingStartMs: recordingStartMS, lastActivity: sess.StartedAt,
	}
	m.mu.Lock()
	m.live[sess.ID] = live
	m.mu.Unlock()

	m.log.Info("会话就绪", "session", sess.ID,
		"cdp", sess.CDPPort, "vnc", sess.VNCPort, "container", containerName)
	return sess, nil
}

// containerNameOf 会话对应的容器名（唯一来源：调度器到处要用它拼 docker 命令）。
func containerNameOf(id string) string { return "rigd-" + id }

// containerSpec 是一次会话要起的那个容器：环境变量、挂载、共享网络、容量。
//
// 单独一个函数是因为这里全是"配错了才会发现"的东西（少一个 RIG_ 变量、
// 挂错只读），和"起容器、等就绪"的流程混在一起时，读的人分不清哪行是契约哪行是流程。
func (m *Manager) containerSpec(sess store.Session, payload payloadInput, containerName string) dockerx.Spec {
	env := map[string]string{
		"RIG_OUT":        "/out",
		"RIG_W":          fmt.Sprint(m.cfg.Display.Width),
		"RIG_H":          fmt.Sprint(m.cfg.Display.Height),
		"RIG_FPS":        fmt.Sprint(m.cfg.Record.FPS),
		"RIG_DEBUG_PORT": fmt.Sprint(sess.CDPPort),
		"RIG_VNC_PORT":   fmt.Sprint(sess.VNCPort),
		"RIG_APP_NAME":   sess.App,
		"RIG_APP_DIR":    "/app",
		"RIG_ENTRY":      payload.Entry,
		"RIG_READY_CDP":  fmt.Sprint(payload.Recipe.Ready.CDP),
		"RIG_READY_SEC":  fmt.Sprint(payload.Recipe.Ready.TimeoutSec),
		"DISPLAY_NUM":    fmt.Sprint(m.displayOf(sess.Slot)),
		"RIG_ARGS":       encodeArgs(payload.Recipe.Args),
	}
	for key, value := range payload.Recipe.Env {
		env[key] = value
	}
	for _, asset := range payload.Assets {
		if asset.Slot.Env != "" {
			env[asset.Slot.Env] = asset.Slot.Path
		}
	}

	mounts := []dockerx.Mount{
		{Source: sess.Dir, ContainerPath: "/out"},
		// 产物只读：会话里被写坏的话，内容寻址库里那份（硬链接指向的）也就坏了
		{Source: payload.Dir, ContainerPath: "/app", ReadOnly: true},
	}
	for _, asset := range payload.Assets {
		// 只读是默认：会话里被写坏的素材，下一次验收就不是同一个输入了。
		// rig.json 写了 writable 的槽位才挂成可写（应用把"不可写"当成没权限时必需）。
		mounts = append(mounts, dockerx.Mount{
			Source: asset.Dir, ContainerPath: asset.Slot.Path, ReadOnly: !asset.Slot.Writable,
		})
	}

	return dockerx.Spec{
		Name:        containerName,
		Image:       m.cfg.Image,
		Cmd:         []string{"rig-serve"},
		NetworkHost: true,
		ShmSize:     "512m",
		Labels:      dockerx.DefaultLabels(sess.ID),
		Env:         env,
		Mounts:      mounts,
	}
}

// encodeArgs 把 rig.json 里的启动参数编成容器读的那份 JSON。
func encodeArgs(args []string) string {
	if len(args) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// abortProvision 应用没能就绪时的收尾：留下容器日志、删掉容器、把会话记为失败。
//
// 日志必须在删容器之前取：应用为什么没起来，答案只在它的 stdout/stderr 里。
func (m *Manager) abortProvision(ctx context.Context, sess store.Session, containerName string, readyErr error) store.Session {
	detail := fmt.Sprintf("等待应用就绪失败: %v", readyErr)
	if path, err := m.archiveContainerLogs(ctx, containerName, sess.Dir); err == nil && path != "" {
		detail = fmt.Sprintf("%s\n容器日志：logs/container.log", detail)
	}
	if err := m.docker.Remove(ctx, containerName); err != nil {
		m.log.Warn("删除失败容器时出错", "session", sess.ID, "err", err)
	}
	sess = m.fail(ctx, sess, detail)
	if err := m.docker.Stop(ctx, containerName, 10*time.Second); err != nil {
		m.log.Warn("停掉未就绪的容器失败", "session", sess.ID, "err", err)
	}
	m.log.Error("会话启动失败", "session", sess.ID, "err", readyErr)
	return sess
}

// recordGeometry 现量一次画面几何存进会话；量不到就按整幅画面显示。
//
// 这是证据的一部分（记录按它裁剪片段），所以每次都记日志，别让它悄悄退化。
func (m *Manager) recordGeometry(ctx context.Context, sess store.Session, containerName string) store.Session {
	frame, display, err := m.probeWindowGeometry(ctx, containerName, sess.Slot)
	if err != nil {
		m.log.Warn("读不到画面几何，界面将按整幅画面显示", "session", sess.ID, "err", err)
		return sess
	}
	sess.Frame, sess.Display = frame, display
	m.log.Info("画面几何", "session", sess.ID,
		"window", fmt.Sprintf("%dx%d", frame.Width, frame.Height),
		"display", fmt.Sprintf("%dx%d", display.Width, display.Height))
	return sess
}

// Stop 收尾会话：停容器（让容器内录屏正常收尾）→ 切片段 → 归档。
//
// 这里没有"结论"这种参数：记录的职责是呈现发生了什么，谁觉得这次够不够格，
// 由读记录的人自己决定。
func (m *Manager) Stop(ctx context.Context, id string) (store.Session, error) {
	return m.StopWithNote(ctx, id, "")
}

// StopWithNote 与 Stop 相同，另外写一句"这次是怎么结束的"。
//
// 只有服务端自己收尾时才会用（超时回收、进程退出、容器意外死掉）：
// 那不是结论，是读者需要知道的事实——为什么这次会话没有走到最后一步。
func (m *Manager) StopWithNote(ctx context.Context, id, note string) (store.Session, error) {
	live, ok := m.liveOf(id)
	if !ok {
		// 不在内存里：可能已被超时回收或进程重启，回落到库里的记录
		sess, err := m.store.GetSession(ctx, id)
		if err != nil {
			return store.Session{}, err
		}
		if sess.Status.Terminal() {
			return sess, nil
		}
		return m.finalize(ctx, sess, note)
	}

	sess := live.session
	sess.Status = store.StatusStopping
	_ = m.store.UpdateSession(ctx, sess)

	containerName := containerNameOf(id)
	if err := m.docker.Stop(ctx, containerName, 25*time.Second); err != nil && !errors.Is(err, dockerx.ErrContainerNotFound) {
		m.log.Warn("停止容器失败", "session", id, "err", err)
	}
	// 容器退出后才有完整日志：先归档，再删除
	if path, err := m.archiveContainerLogs(ctx, containerName, sess.Dir); err != nil {
		m.log.Warn("归档容器日志失败", "session", id, "err", err)
	} else if path != "" {
		m.log.Info("容器日志已归档", "session", id, "path", path)
	}
	if err := m.docker.Remove(ctx, containerName); err != nil {
		m.log.Warn("删除容器失败", "session", id, "err", err)
	}
	if live.inspector != nil {
		_ = live.inspector.Close()
	}

	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()

	return m.finalize(ctx, sess, note)
}

// finalize 收尾的公共部分：读录像元信息、切片段、落库。
func (m *Manager) finalize(ctx context.Context, sess store.Session, note string) (store.Session, error) {
	sess.Status = store.StatusFinished
	sess.EndedAt = time.Now()
	if note != "" {
		sess.Note = note
	}

	m.releaseSources(ctx, sess)

	// 产物/素材快照是"输入"，不是证据：会话收尾就删，别把 1GB 的应用复制留在证据目录里。
	// 进程被杀留下的残留由启动时的 CleanupSnapshots 兜底。
	_ = os.RemoveAll(filepath.Join(sess.Dir, "payload"))
	_ = os.RemoveAll(filepath.Join(sess.Dir, "assets"))

	videoPath := filepath.Join(sess.Dir, "video", "session.mp4")
	if info, err := os.Stat(videoPath); err == nil && info.Size() > 0 {
		if meta, err := readVideoMeta(filepath.Join(sess.Dir, "video", "session.json")); err == nil {
			sess.VideoDurationMS = meta.DurationMS
		}
		// 先切片段，记录里"这一段的画面"靠它们（小、秒开）。
		if n, err := m.cutClips(ctx, sess); err != nil {
			m.log.Warn("切片段失败，部分步骤将没有画面", "session", sess.ID, "err", err)
		} else {
			m.log.Info("片段已归档", "session", sess.ID, "clips", n)
		}
		// 母片搬到归档区而不是删掉：它是原始现场，AI 事后重新取帧、核对时序都靠它。
		// 页面不放它，"存储"页能看到它占多少，也可以整体清空。
		if err := os.MkdirAll(filepath.Dir(archiveVideo(sess)), 0o755); err != nil {
			m.log.Warn("创建归档目录失败", "session", sess.ID, "err", err)
		} else if err := os.Rename(videoPath, archiveVideo(sess)); err != nil {
			m.log.Warn("归档整场录像失败", "session", sess.ID, "err", err)
		} else {
			m.log.Info("整场录像已归档", "session", sess.ID, "path", filepath.Join("archive", "session.mp4"))
		}
	}
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		return sess, err
	}
	// 时间线：把事件表投影成一份机器可读的文件，和母片放在一起
	if err := m.writeTimeline(ctx, sess); err != nil {
		m.log.Warn("导出时间线失败", "session", sess.ID, "err", err)
	}
	m.log.Info("会话收尾完成", "session", sess.ID, "duration_ms", sess.VideoDurationMS)
	return sess, nil
}

// Get 取会话（内存里有就用最新的）。
func (m *Manager) Get(ctx context.Context, id string) (store.Session, error) {
	if live, ok := m.liveOf(id); ok {
		return live.session, nil
	}
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

// Sweep 回收超时会话。返回被回收的会话 ID。
//
// 超时不是直接删容器：先走正常收尾（停录屏、归档录像），再标记 abandoned。
func (m *Manager) Sweep(ctx context.Context) []string {
	// 一次遍历同时得出两件事：该回收的、以及要巡检容器死活的。
	// 分两趟加锁除了多写一遍遍历，还会让两趟看到的状态不是同一个瞬间。
	now := time.Now()
	m.mu.Lock()
	expired := make([]string, 0, len(m.live))
	aliveCheck := make([]string, 0, len(m.live))
	for id, live := range m.live {
		idleExpired := !live.session.IdleDeadline.IsZero() && now.After(live.session.IdleDeadline)
		lifetimeExpired := now.Sub(live.session.StartedAt) > m.cfg.MaxLifetime
		idleByActivity := now.Sub(live.lastActivity) > m.cfg.IdleTimeout
		if idleExpired || lifetimeExpired || idleByActivity {
			expired = append(expired, id)
		}
		if live.session.Status == store.StatusReady {
			aliveCheck = append(aliveCheck, id)
		}
	}
	m.mu.Unlock()

	// 另外巡检容器是否还活着（外部 docker rm / OOM 不会通知我们）
	for _, id := range aliveCheck {
		m.checkContainerAlive(ctx, id)
	}

	reclaimed := make([]string, 0, len(expired))
	for _, id := range expired {
		m.log.Warn("会话超时，开始回收", "session", id)
		if _, err := m.StopWithNote(ctx, id, "会话超时被回收（可能客户端已断开）"); err != nil {
			m.log.Error("回收会话失败", "session", id, "err", err)
			continue
		}
		if sess, err := m.store.GetSession(ctx, id); err == nil {
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
	DurationMS int64   `json:"durationMs"`
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
			m.fail(ctx, sess, fmt.Sprintf("控制面重启时该会话处于 %s 状态，已放弃接管", sess.Status))
			continue
		}

		// 容器没了就标记失败，别让它一直挂在"运行中"
		state, err := m.docker.Inspect(ctx, containerNameOf(sess.ID))
		if err != nil || !state.Running {
			m.fail(ctx, sess, "控制面重启时容器已不在运行")
			continue
		}

		// 接管必须快：端口连不上就放弃，不能把启动流程堵在这里（原来会阻塞 5 分钟）
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		inspector, dialErr := m.factory(dialCtx, sess.CDPPort)
		cancel()
		if dialErr == nil {
			live.inspector = inspector
			// 接管时同样不碰窗口，只重新量一次几何（应用重启后可能换了尺寸）
			if frame, display, geoErr := m.probeWindowGeometry(ctx, containerNameOf(sess.ID), sess.Slot); geoErr == nil {
				sess.Frame = frame
				sess.Display = display
				_ = m.store.UpdateSession(ctx, sess)
			}
		} else {
			m.log.Warn("接管会话时连接调试端口失败，会话标记为失败", "session", sess.ID, "err", dialErr)
			m.fail(ctx, sess, fmt.Sprintf("接管失败：%v", dialErr))
			continue
		}
		if ms, err := m.readRecordingStart(ctx, containerNameOf(sess.ID)); err == nil {
			live.recordingStartMs = ms
			if sess.RecordingStartMs != ms {
				sess.RecordingStartMs = ms
				_ = m.store.UpdateSession(ctx, sess)
			}
		}

		m.mu.Lock()
		// live.session 要跟着改完的副本走（几何/commit 都是接管后才补上的）
		live.session = sess
		m.live[sess.ID] = live
		m.mu.Unlock()
		recovered++
	}
	if recovered > 0 {
		m.log.Info("已接管仍在运行的会话", "count", recovered)
	}
	return recovered, nil
}

// isTransportError 判断错误是不是"连接层挂了"，而不是页面里的业务错误。
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"use of closed network connection", "connection reset", "broken pipe",
		"EOF", "CDP 连接已关闭", "i/o timeout", "connection refused",
		// 页面自己导航/刷新（eval 里 location.reload、应用内部跳转）会换掉调试目标：
		// 这不是"操作失败"，重连一次就能接着往下走。
		"Inspected target navigated", "Cannot find context with specified id",
		"Execution context was destroyed", "Target closed",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// dropInspector 丢掉缓存的检查器，下次使用时重新建连。
func (m *Manager) dropInspector(id string) {
	m.mu.Lock()
	if live, ok := m.live[id]; ok {
		if live.inspector != nil {
			_ = live.inspector.Close()
		}
		live.inspector = nil
	}
	m.mu.Unlock()
	m.log.Info("已清除失效的检查器连接", "session", id)
}

// checkContainerAlive 巡检：容器没了就把会话收尾，避免"显示 ready 但点不动"。
func (m *Manager) checkContainerAlive(ctx context.Context, id string) {
	state, err := m.docker.Inspect(ctx, containerNameOf(id))
	if err == nil && state.Running {
		return
	}
	m.log.Warn("容器已不在运行，自动收尾会话", "session", id)
	if _, err := m.StopWithNote(ctx, id, "容器意外退出，已自动收尾"); err != nil {
		m.log.Error("自动收尾失败", "session", id, "err", err)
	}
	if sess, err := m.store.GetSession(ctx, id); err == nil {
		m.fail(ctx, sess, "运行期间容器意外退出")
	}
}

// archiveContainerLogs 把容器 stdout/stderr 落到会话目录，返回文件路径。
//
// 应用为什么没起来、为什么白屏，答案通常只在这里；容器用的是 --rm 之外的策略，
// 所以必须在删除容器之前做这一步。
func (m *Manager) archiveContainerLogs(ctx context.Context, container, sessionDir string) (string, error) {
	logs, err := m.docker.Logs(ctx, container, 4000)
	if err != nil && strings.TrimSpace(logs) == "" {
		return "", err
	}
	if strings.TrimSpace(logs) == "" {
		return "", nil
	}
	path := filepath.Join(sessionDir, "logs", "container.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(logs), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// syncFrame 对一次窗口几何：和上次不同就更新会话的 frame。
//
// 为什么不只在就绪时量一次：应用的登录/引导结束时会自己切窗口状态
// （实测 DashPlayer 点「Start using」后从 1920×1080 缩回 960×540），
// 之后录像里就是「小窗 + 大桌面」，显示层还按旧的几何裁剪。
func (m *Manager) syncFrame(ctx context.Context, id string, insp Inspector) {
	measurer, ok := insp.(interface {
		Viewport(ctx context.Context) (int, int, error)
	})
	if !ok {
		return
	}
	width, height, err := measurer.Viewport(ctx)
	if err != nil || width <= 0 || height <= 0 {
		return
	}

	// 先用"视口和上次是否一样"判断有没有变化——视口每帧都能读到，便宜且不会误报。
	m.mu.Lock()
	live, alive := m.live[id]
	if !alive {
		m.mu.Unlock()
		return
	}
	changed := live.viewportW != width || live.viewportH != height
	live.viewportW, live.viewportH = width, height
	m.mu.Unlock()
	if !changed {
		return
	}

	sess, err := m.Get(ctx, id)
	if err != nil {
		return
	}
	// 视口变了，现量一次精确的窗口矩形（含位置）；量不到就按左上角+视口尺寸算
	frame := store.Rect{X: sess.Frame.X, Y: sess.Frame.Y, Width: width, Height: height}
	display := sess.Display
	if exact, gotDisplay, geoErr := m.probeWindowGeometry(ctx, containerNameOf(id), sess.Slot); geoErr == nil {
		frame, display = exact, gotDisplay
	}
	if !display.Known() {
		display = store.Rect{Width: m.cfg.Display.Width, Height: m.cfg.Display.Height}
	}
	prev := sess.Frame
	if prev.Known() && prev == frame {
		return // 视口变了但窗口矩形没变（滚动条出现/消失），不是真的改窗口
	}
	sess.Frame, sess.Display = frame, display
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		m.log.Warn("更新画面几何失败", "session", id, "err", err)
		return
	}
	m.mu.Lock()
	if current, ok := m.live[id]; ok {
		current.session.Frame = sess.Frame
		current.session.Display = sess.Display
	}
	m.mu.Unlock()

	// 这是证据，不是内部细节：应用自己把窗口缩小了，评审必须看得见。
	// 记成记录里独立的一步（带当时的画面），而不是一行服务端日志。
	note := Step{
		Tool: "window",
		Kind: "note",
		Args: map[string]any{
			"from": fmt.Sprintf("%dx%d", prev.Width, prev.Height),
			"to":   fmt.Sprintf("%dx%d", frame.Width, frame.Height),
		},
		Caption: fmt.Sprintf("窗口尺寸变化：%dx%d → %dx%d", prev.Width, prev.Height, frame.Width, frame.Height),
	}
	note.Shot = m.captureShot(ctx, id, sess.Dir, insp, note)
	m.writeEvent(ctx, id, note, eventOutcome{
		Result: "应用自己调整了窗口尺寸，画面几何已更新", OK: true, OffsetMS: m.NowOffset(id),
	})
	m.log.Info("窗口尺寸变化", "session", id,
		"from", fmt.Sprintf("%dx%d", prev.Width, prev.Height),
		"to", fmt.Sprintf("%dx%d", frame.Width, frame.Height))
}
