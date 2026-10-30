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

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/mcpserver"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// MCPInfo 管理台「MCP」页要展示的连接信息。
type MCPInfo struct {
	// HTTPURL 远程接入地址（Streamable HTTP）。
	HTTPURL string
	// Token 访问令牌（Authorization: Bearer）。
	Token string
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

// Handler 返回带中间件的根路由器。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "sessions": len(s.activeIDs())})
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
		api.Get("/sessions/{id}/video", s.serveVideo)
		api.Get("/sessions/{id}/artifacts/{name}", s.serveArtifact)
		api.Get("/sessions/stream", s.streamSessions)
		api.Get("/sessions/{id}/screen.mjpg", s.screenStream)
		api.Get("/apps", s.apps)
		api.Get("/machines", s.machines)
		api.Get("/info", s.info)
		api.Get("/mcp", s.mcpInfo)
		api.Get("/system/metrics", s.systemMetrics)
		api.Get("/system/stream", s.streamSystem)
	})

	r.NotFound(s.serveSPA)
	return r
}

// ---- 会话 ----------------------------------------------------------------

type sessionDTO struct {
	ID              string `json:"id"`
	App             string `json:"app"`
	Ref             string `json:"ref"`
	CommitSHA       string `json:"commitSha"`
	Status          string `json:"status"`
	CDPPort         int    `json:"cdpPort"`
	VNCPort         int    `json:"vncPort"`
	CreatedAt       string `json:"createdAt"`
	StartedAt       string `json:"startedAt,omitempty"`
	EndedAt         string `json:"endedAt,omitempty"`
	IdleDeadline    string `json:"idleDeadline,omitempty"`
	HasVideo        bool   `json:"hasVideo"`
	VideoDurationMS int64  `json:"videoDurationMs"`
	Verdict         string `json:"verdict,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Error           string `json:"error,omitempty"`
}

func toSessionDTO(s store.Session) sessionDTO {
	dto := sessionDTO{
		ID: s.ID, App: s.App, Ref: s.Ref, CommitSHA: s.CommitSHA, Status: string(s.Status),
		CDPPort: s.CDPPort, VNCPort: s.VNCPort, VideoDurationMS: s.VideoDurationMS,
		Verdict: s.Verdict, Summary: s.Summary, Error: s.Error,
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
	if s.VideoPath != "" {
		if info, err := os.Stat(s.VideoPath); err == nil && info.Size() > 0 {
			dto.HasVideo = true
		}
	}
	return dto
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
	out := make([]sessionDTO, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, toSessionDTO(sess))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

type createRequest struct {
	App string `json:"app"`
	Ref string `json:"ref"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.App) == "" {
		writeError(w, http.StatusBadRequest, errors.New("app 不能为空"))
		return
	}
	if req.Ref == "" {
		req.Ref = "main"
	}

	// 起会话要 clone + 装依赖 + 构建 + 启动应用，动辄几分钟：
	// 这里只做同步登记并立刻返回 202，真正的启动在后台跑，进度通过 SSE 推给前端。
	sess, err := s.manager.Prepare(r.Context(), session.Options{App: req.App, Ref: req.Ref})
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
	DurationMS    int64           `json:"durationMs"`
	VideoOffsetMS int64           `json:"videoOffsetMs"`
	Result        string          `json:"result"`
	Artifact      string          `json:"artifact,omitempty"`
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.manager.Events(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]eventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, eventDTO{
			Seq: e.Seq, At: e.At.UTC().Format(time.RFC3339Nano), Tool: e.Tool, Args: e.Args,
			OK: e.OK, DurationMS: e.DurationMS, VideoOffsetMS: e.VideoOffsetMS,
			Result: e.Result, Artifact: e.ArtifactPath,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// serveVideo 提供录像；http.ServeFile 自带 Range，支持前端拖进度条。
func (s *Server) serveVideo(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	path := sess.VideoPath
	if path == "" {
		path = filepath.Join(sess.Dir, "video", "session.mp4")
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, errors.New("该会话没有录像"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// serveArtifact 提供截图等产物。路径做白名单校验，禁止跳出会话目录。
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	name := chi.URLParam(r, "name")
	if strings.Contains(name, "..") || strings.Contains(name, "/") {
		writeError(w, http.StatusBadRequest, errors.New("非法文件名"))
		return
	}
	path := filepath.Join(sess.Dir, "screenshots", name)
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
		out := make([]sessionDTO, 0, len(sessions))
		for _, sess := range sessions {
			out = append(out, toSessionDTO(sess))
		}
		return map[string]any{"sessions": out}
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

func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	limit := 120
	samples, err := s.store.RecentSamples(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]sampleDTO, 0, len(samples))
	for _, m := range samples {
		out = append(out, sampleDTO{
			At: m.At.UTC().Format(time.RFC3339), CPUPct: m.CPUPct,
			MemUsed: m.MemUsed, MemTotal: m.MemTotal,
			DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, Containers: m.Containers,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": out})
}

func (s *Server) streamSystem(w http.ResponseWriter, r *http.Request) {
	s.streamSnapshot(w, r, func(ctx context.Context) any {
		samples, err := s.store.RecentSamples(ctx, 120)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		out := make([]sampleDTO, 0, len(samples))
		for _, m := range samples {
			out = append(out, sampleDTO{
				At: m.At.UTC().Format(time.RFC3339), CPUPct: m.CPUPct,
				MemUsed: m.MemUsed, MemTotal: m.MemTotal,
				DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, Containers: m.Containers,
			})
		}
		return map[string]any{"samples": out}
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

// activeIDs 仅用于 readyz 的粗略展示。
func (s *Server) activeIDs() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sessions, err := s.store.ActiveSessions(ctx)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		ids = append(ids, sess.ID)
	}
	return ids
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

// apps 应用注册表：控制台据此渲染应用选择器，不在前端写死任何一个应用名。
func (s *Server) apps(w http.ResponseWriter, _ *http.Request) {
	entries := session.AppCatalog()
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any{
			"key":         entry.Key,
			"name":        entry.Spec.Name,
			"repo":        entry.Spec.Repo,
			"description": entry.Spec.Description,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
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
		"maxSessions": s.cfg.MaxSessions,
		"idleTimeout": s.cfg.IdleTimeout.String(),
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
