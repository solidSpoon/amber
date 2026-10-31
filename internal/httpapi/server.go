// Package httpapi 提供管理台的 REST/SSE 接口，并托管内嵌的 React 前端。
//
// 设计取舍：
//   - 只做「薄适配」：校验参数、调用 session.Manager、把结果转成 DTO，业务逻辑都在 Manager 里；
//   - 会话操作全部有副作用，因此除查询外都用 POST，避免被浏览器预取误触发；
//   - 录像走 http.ServeFile，天然支持 Range，前端拖进度条不需要额外实现；
//   - SSE 用轮询式推送（每 2s 推一次快照），比给每个会话建长连接简单得多，也够用。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/solidSpoon/rigd/docs"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/mcpserver"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// MCPInfo 管理台「MCP」页要展示的连接信息。
type MCPInfo struct {
	// HTTPURL 远程接入地址（Streamable HTTP）。
	HTTPURL string
	// Command stdio 方式的启动命令。
	Command []string
	// Env stdio 方式需要带的环境变量。
	Env map[string]string
	// Tools 当前进程实际暴露的工具（由 MCP 自己 tools/list 得到，不手写）。
	Tools []mcpserver.ToolInfo
	// HTTPEnabled 是否挂载了 HTTP 接入点。
	HTTPEnabled bool
}

// Options 构造参数。
type Options struct {
	Config  *config.Config
	Store   *store.Store
	Manager *session.Manager
	WebFS   fs.FS
	Log     *slog.Logger
	MCP     MCPInfo
}

// Server 组装路由。
type Server struct {
	cfg     *config.Config
	store   *store.Store
	manager *session.Manager
	webFS   fs.FS
	log     *slog.Logger
	mcp     MCPInfo
}

// New 构造 HTTP 服务。WebFS 为 nil 时前端路由返回 404（API 仍可用）。
func New(opts Options) *Server {
	return &Server{
		cfg: opts.Config, store: opts.Store, manager: opts.Manager,
		webFS: opts.WebFS, log: opts.Log, mcp: opts.MCP,
	}
}

// requestTimeout 单个 HTTP 请求的上限：批上限再留 30 秒收尾（收尾那张图要现抓）。
func (s *Server) requestTimeout() time.Duration {
	base := 2 * time.Minute
	if s.cfg != nil && s.cfg.BatchTimeout > 0 {
		base = s.cfg.BatchTimeout
	}
	return base + 30*time.Second
}

// Handler 返回带中间件的根路由器。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// 中间件的超时必须比一批动作的上限更宽：否则请求先被这里砍断，
	// 客户端拿到一句没有上下文的错误，服务端只剩一行 context canceled。
	r.Use(middleware.Timeout(s.requestTimeout()))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "sessions": s.activeSessionCount()})
	})

	r.Route("/api", func(api chi.Router) {
		api.Get("/sessions", s.listSessions)
		api.Post("/sessions", s.createSession)
		api.Get("/sessions/{id}", s.getSession)
		api.Post("/sessions/{id}/stop", s.stopSession)
		api.Get("/sessions/{id}/events", s.listEvents)
		api.Get("/sessions/{id}/logs", s.listLogs)
		api.Get("/sessions/{id}/logs/{source}", s.getLog)
		api.Post("/sessions/{id}/snapshot", s.actionSnapshot)
		api.Post("/sessions/{id}/screenshot", s.actionScreenshot)
		api.Post("/sessions/{id}/eval", s.actionEval)
		api.Post("/sessions/{id}/click", s.actionClick)
		api.Post("/sessions/{id}/fill", s.actionFill)
		api.Post("/sessions/{id}/press", s.actionPress)
		api.Get("/sessions/{id}/events/{seq}/clip", s.serveClip)
		// 用通配符而不是单段参数：产物路径带目录（shots/x.jpg、screenshots/x.png）
		api.Get("/sessions/{id}/artifacts/*", s.serveArtifact)
		api.Get("/sessions/stream", s.streamSessions)
		api.Get("/sessions/{id}/screen.mjpg", s.screenStream)
		api.Get("/machines", s.machines)
		api.Get("/info", s.info)
		api.Get("/payloads", s.listPayloads)
		api.Post("/blobs/missing", s.missingBlobs)
		api.Put("/blobs/{digest}", s.putBlob)
		api.Post("/manifests", s.putManifest)
		api.Get("/mcp", s.mcpInfo)
		api.Get("/docs/product", s.productDoc)
		api.Get("/feedback", s.listFeedback)
		api.Get("/system/metrics", s.systemMetrics)
		api.Get("/system/stream", s.streamSystem)
	})

	r.NotFound(s.serveSPA)
	return r
}

// ---- 会话 ----------------------------------------------------------------

type sessionDTO struct {
	ID string `json:"id"`
	// Title 这次验收的意图（一句话），列表和记录顶部先展示它
	Title string `json:"title,omitempty"`
	App   string `json:"app"`
	// Ref 来源版本号（commit / tag / build id），只用于溯源
	Ref          string `json:"ref"`
	Status       string `json:"status"`
	CDPPort      int    `json:"cdpPort"`
	VNCPort      int    `json:"vncPort"`
	CreatedAt    string `json:"createdAt"`
	StartedAt    string `json:"startedAt,omitempty"`
	EndedAt      string `json:"endedAt,omitempty"`
	IdleDeadline string `json:"idleDeadline,omitempty"`
	// VideoDurationMS 这次会话录了多久；整场录像本身不保留（见 session.finalize）
	VideoDurationMS int64 `json:"videoDurationMs"`
	// Payload / Assets 是这次验的是哪份产物、挂了哪些素材
	Payload string            `json:"payload,omitempty"`
	Assets  map[string]string `json:"assets,omitempty"`
	// Frame / Display 是画面几何：前人是「有内容的区域」（= 应用窗口），
	// 后人是「录像的整幅像素」。两者不同就说明录像里有应当被裁掉的桌面。
	Frame   *geometryDTO `json:"frame,omitempty"`
	Display *geometryDTO `json:"display,omitempty"`
	Verdict string       `json:"verdict,omitempty"`
	Summary string       `json:"summary,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type geometryDTO struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// decodeAssetsJSON 把库里存的槽位映射还原给前端（坏数据当空）。
func decodeAssetsJSON(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var assets map[string]string
	if err := json.Unmarshal([]byte(raw), &assets); err != nil {
		return nil
	}
	return assets
}

func toGeometry(r store.Rect) *geometryDTO {
	if !r.Known() {
		return nil
	}
	return &geometryDTO{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
}

func toSessionDTO(s store.Session) sessionDTO {
	dto := sessionDTO{
		ID: s.ID, Title: s.Title, App: s.App, Ref: s.Ref, Status: string(s.Status),
		CDPPort: s.CDPPort, VNCPort: s.VNCPort, VideoDurationMS: s.VideoDurationMS,
		Verdict: s.Verdict, Summary: s.Summary, Error: s.Error,
		Frame: toGeometry(s.Frame), Display: toGeometry(s.Display),
		Payload: s.Payload, Assets: decodeAssetsJSON(s.Assets),
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !s.StartedAt.IsZero() {
		dto.StartedAt = s.StartedAt.UTC().Format(time.RFC3339)
	}
	if !s.EndedAt.IsZero() {
		dto.EndedAt = s.EndedAt.UTC().Format(time.RFC3339)
	}
	if !s.IdleDeadline.IsZero() {
		dto.IdleDeadline = s.IdleDeadline.UTC().Format(time.RFC3339)
	}
	return dto
}

// toSessionDTOs 把一次会话列表整体转成 DTO：列表接口和 SSE 推的是同一份形状。
func toSessionDTOs(sessions []store.Session) []sessionDTO {
	out := make([]sessionDTO, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, toSessionDTO(sess))
	}
	return out
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	sessions, err := s.manager.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": toSessionDTOs(sessions)})
}

type createRequest struct {
	// Payload 被测产物：dir:/绝对路径 或 manifest:sha256:…
	Payload string `json:"payload"`
	// Assets 素材槽位 → 来源（槽位由产物自己的 rig.json 声明）
	Assets map[string]string `json:"assets"`
	Label  string            `json:"label"`
	// Title 这次验收的意图；记录顶部与会话列表用它当标题
	Title  string `json:"title"`
	Commit string `json:"commit"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Payload) == "" {
		writeError(w, http.StatusBadRequest, errors.New("payload 不能为空"))
		return
	}

	// 产物已经在 Prepare 里落地并校验过（入口、素材槽位），所以这里失败得快；
	// 真正的启动仍然在后台跑，进度通过 SSE 推给前端。
	sess, err := s.manager.Prepare(r.Context(), session.Options{
		Label: req.Label, Title: req.Title, Payload: req.Payload, Assets: req.Assets, Commit: req.Commit,
	})
	if errors.Is(err, session.ErrTooManySessions) {
		writeError(w, http.StatusTooManyRequests, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	go func() {
		// 用独立 context：创建请求结束（客户端断开）不应该中断已经开始的启动流程
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := s.manager.Provision(ctx, sess); err != nil {
			s.log.Error("会话启动失败", "session", sess.ID, "err", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, toSessionDTO(sess))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDTO(sess))
}

type stopRequest struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
			return
		}
	}
	sess, err := s.manager.Stop(r.Context(), chi.URLParam(r, "id"), req.Verdict, req.Summary)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDTO(sess))
}

type eventDTO struct {
	Seq           int             `json:"seq"`
	At            string          `json:"at"`
	Tool          string          `json:"tool"`
	Args          json.RawMessage `json:"args"`
	OK            bool            `json:"ok"`
	Kind          string          `json:"kind"`
	Caption       string          `json:"caption"`
	Intent        string          `json:"intent,omitempty"`
	Expect        string          `json:"expect,omitempty"`
	DurationMS    int64           `json:"durationMs"`
	VideoOffsetMS int64           `json:"videoOffsetMs"`
	Result        string          `json:"result"`
	Artifact      string          `json:"artifact,omitempty"`
	// Shot 这一步的画面；Clip 表示有没有可点的 4 秒短片
	Shot string `json:"shot,omitempty"`
	Clip bool   `json:"clip"`
	// Frame 这一步当时窗口的矩形（裁剪用；窗口中途变过也不会错位）
	Frame *geometryDTO `json:"frame,omitempty"`
	// BatchStartMS/BatchEndMS 这一段连续操作的起止：同一批的几步共用一条片段
	BatchStartMS int64 `json:"batchStartMs"`
	BatchEndMS   int64 `json:"batchEndMs"`
	// BatchIntent 这一段操作为什么做（run 的 intent）：记录里当这一段的小标题
	BatchIntent string `json:"batchIntent,omitempty"`
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.manager.Events(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sess, _ := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	out := make([]eventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, eventDTO{
			Seq: e.Seq, At: e.At.UTC().Format(time.RFC3339Nano), Tool: e.Tool, Args: e.Args,
			OK: e.OK, Kind: e.Kind, Caption: e.Caption, Intent: e.Intent, Expect: e.Expect,
			DurationMS: e.DurationMS, VideoOffsetMS: e.VideoOffsetMS,
			Result: e.Result, Artifact: e.ArtifactPath, Shot: e.Shot,
			Clip: s.manager.ClipReady(sess, e), Frame: toGeometry(e.Frame),
			BatchStartMS: e.BatchStartMS, BatchEndMS: e.BatchEndMS, BatchIntent: e.BatchIntent,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// listFeedback 最近的 AI 建议（给控制台看，也是为了后续集中分析）。
func (s *Server) listFeedback(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, err := s.store.ListFeedback(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, f := range items {
		out = append(out, map[string]any{
			"id":        f.ID,
			"at":        f.At.UTC().Format(time.RFC3339Nano),
			"sessionId": f.SessionID,
			"source":    f.Source,
			"rating":    f.Rating,
			"body":      f.Body,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": out})
}

// productDoc 返回产品文档原文：页面按 Markdown 渲染，文档本身只有一份（docs/PRODUCT.md）。
func (s *Server) productDoc(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = io.WriteString(w, docs.Product)
}

// serveClip 某一步所在那一批的短片（收尾时已切好，直接命中缓存）。
func (s *Server) serveClip(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("seq 必须是数字"))
		return
	}
	path, err := s.manager.Clip(r.Context(), chi.URLParam(r, "id"), seq)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// 切片一旦生成就不会变
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

// serveArtifact 提供截图等产物。路径做白名单校验，禁止跳出会话目录。
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// 路径形如 "screenshots/x.png" 或 "shots/x.jpg"；不带目录的按截图处理（老链接）。
	// 只允许这两个目录 + 单层文件名，防止路径穿越。
	name := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	dir, file, hasDir := strings.Cut(name, "/")
	if !hasDir {
		dir, file = "screenshots", name
	}
	if (dir != "screenshots" && dir != "shots") || file == "" || strings.ContainsAny(file, "/\\") || strings.Contains(file, "..") {
		writeError(w, http.StatusBadRequest, errors.New("非法产物路径"))
		return
	}
	path := filepath.Join(sess.Dir, dir, file)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	http.ServeFile(w, r, path)
}

// streamSessions 每 2s 推一次会话列表快照（SSE）。
func (s *Server) streamSessions(w http.ResponseWriter, r *http.Request) {
	s.streamSnapshot(w, r, func(ctx context.Context) any {
		sessions, err := s.manager.List(ctx, 50)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"sessions": toSessionDTOs(sessions)}
	})
}

type sampleDTO struct {
	At         string  `json:"at"`
	CPUPct     float64 `json:"cpuPct"`
	MemUsed    uint64  `json:"memUsed"`
	MemTotal   uint64  `json:"memTotal"`
	DiskUsed   uint64  `json:"diskUsed"`
	DiskTotal  uint64  `json:"diskTotal"`
	Containers int     `json:"containers"`
}

// toSampleDTOs 把采样整体转成 DTO：一次性接口和 SSE 推的是同一份形状。
func toSampleDTOs(samples []store.Sample) []sampleDTO {
	out := make([]sampleDTO, 0, len(samples))
	for _, m := range samples {
		out = append(out, sampleDTO{
			At: m.At.UTC().Format(time.RFC3339), CPUPct: m.CPUPct,
			MemUsed: m.MemUsed, MemTotal: m.MemTotal,
			DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, Containers: m.Containers,
		})
	}
	return out
}

func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	samples, err := s.store.RecentSamples(r.Context(), 120)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": toSampleDTOs(samples)})
}

func (s *Server) streamSystem(w http.ResponseWriter, r *http.Request) {
	s.streamSnapshot(w, r, func(ctx context.Context) any {
		samples, err := s.store.RecentSamples(ctx, 120)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"samples": toSampleDTOs(samples)}
	})
}

// streamSnapshot 是 SSE 的通用实现：定时调用 payload 并把结果推给浏览器。
func (s *Server) streamSnapshot(w http.ResponseWriter, r *http.Request, payload func(ctx context.Context) any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("当前服务器不支持流式响应"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	send := func() bool {
		data, err := json.Marshal(payload(r.Context()))
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

// activeSessionCount 给 readyz 用：只数个数，不必把会话整行读出来。
func (s *Server) activeSessionCount() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := s.store.ActiveSessionCount(ctx)
	if err != nil {
		return 0
	}
	return n
}

// serveSPA 把非 API 路径交给前端路由（单页应用刷新不会 404）。
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, errors.New("接口不存在"))
		return
	}
	if s.webFS == nil {
		writeError(w, http.StatusNotFound, errors.New("前端未内嵌，请先 make web"))
		return
	}
	clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if clean != "" && clean != "." {
		if f, err := s.webFS.Open(clean); err == nil {
			_ = f.Close()
			http.ServeFileFS(w, r, s.webFS, clean)
			return
		}
	}
	http.ServeFileFS(w, r, s.webFS, "index.html")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// info 运行时信息：虚拟屏几何、并发上限等，看板据此画出与真实分辨率同比例的"显示器"。
func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"display": map[string]int{
			"width":  s.cfg.Display.Width,
			"height": s.cfg.Display.Height,
		},
		"record": map[string]int{
			"fps": s.cfg.Record.FPS,
			"crf": s.cfg.Record.CRF,
		},
		"maxSessions":    s.cfg.MaxSessions,
		"idleTimeout":    s.cfg.IdleTimeout.String(),
		"uploadLimitMB":  s.cfg.MaxUploadMB,
		"workspaceRoots": s.cfg.WorkspaceRoots,
	})
}

// machines 看板首页的数据源：固定槽位的两台（或多台）候选机器。
func (s *Server) machines(w http.ResponseWriter, r *http.Request) {
	views, err := s.manager.Machines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": views})
}

// screenStream 把会话画面以 MJPEG 推给看板。
//
// 用 MJPEG 而不是 WebSocket：浏览器里一个 <img> 就能显示，不需要前端写解码循环，
// 断线重连也是浏览器自己处理。帧率故意压到 2fps——看板是"看一眼在干嘛"，
// 更高的帧率只会把 CPU 和带宽吃在无意义的地方。
func (s *Server) screenStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("当前服务器不支持流式响应"))
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	send := func() bool {
		frame, err := s.manager.Preview(r.Context(), id, 70)
		if err != nil {
			return true // 暂时取不到（正在构建/暂停），下一拍再试
		}
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
			return false
		}
		if _, err := w.Write(frame); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}
