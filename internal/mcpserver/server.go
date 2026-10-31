// Package mcp 把会话调度能力暴露成 MCP 工具，让任意支持 MCP 的 AI 客户端来驱动验收。
//
// 这一层的职责只有三件：
//  1. 定义工具契约（参数、说明）；
//  2. 把调用翻译成 session.Manager 的操作；
//  3. 把结果整理成「给模型看的文本 + 需要时附带图片」。
//
// 刻意不在这里保存任何状态：会话状态全部在 Manager/Store 里，
// 这样 AI 断开重连、换一个客户端继续操作，都不影响会话本身。
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// Server 包装 SDK server 与调度器。
type Server struct {
	manager *session.Manager
	store   *store.Store
	log     *slog.Logger
}

// NewServer 注册全部工具并返回可直接 Run 的 MCP server。
func NewServer(mgr *session.Manager, st *store.Store, log *slog.Logger) *mcp.Server {
	s := &Server{manager: mgr, store: st, log: log}
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "rigd",
		Title:       "Acceptance Rig",
		Description: "一次性 GUI 验收环境：起容器、驱动应用、全程录屏、留审计",
		Version:     "0.1.0",
	}, nil)

	registerSessionTools(server, s)
	registerSnapshotTool(server, s)
	registerRunTool(server, s)
	registerFeedbackTool(server, s)

	return server
}

// ---- 会话管理 -------------------------------------------------------------

type startArgs struct {
	// Payload 被测产物：同机用 dir:/绝对路径；跨机先 POST /api/blobs + /api/manifests，再传 manifest:sha256:…
	Payload string `json:"payload" jsonschema:"被测产物：dir:/绝对路径 或 manifest:sha256:…（必填）"`
	// Assets 产物 rig.json 里声明的素材槽位 → 来源
	Assets map[string]string `json:"assets,omitempty" jsonschema:"素材槽位映射，槽位由产物 rig.json 声明"`
	Label  string            `json:"label,omitempty" jsonschema:"展示名；留空用产物 rig.json 里的 name"`
	Commit string            `json:"commit,omitempty" jsonschema:"可选的来源版本号，只用于报告溯源"`
}

type startResult struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	CDPPort   int    `json:"cdpPort"`
	VNCURL    string `json:"vncUrl,omitempty"`
	VideoURL  string `json:"videoUrl"`
}

func registerSessionTools(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "start_session",
		Description: "申请一台一次性测试机：拉起容器、启动被测应用、开始全程录屏。" +
			"返回 sessionId，之后所有操作都要带上它。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args startArgs) (*mcp.CallToolResult, startResult, error) {
		if strings.TrimSpace(args.Payload) == "" {
			return nil, startResult{}, errors.New("payload 不能为空（dir:/绝对路径 或 manifest:sha256:…）")
		}
		sess, err := s.manager.Start(ctx, session.Options{
			Label: args.Label, Payload: args.Payload, Assets: args.Assets, Commit: args.Commit,
		})
		if err != nil {
			return nil, startResult{}, err
		}
		result := startResult{
			SessionID: sess.ID,
			Status:    string(sess.Status),
			CDPPort:   sess.CDPPort,
			VideoURL:  fmt.Sprintf("/api/sessions/%s/video", sess.ID),
		}
		if sess.VNCPort > 0 {
			result.VNCURL = fmt.Sprintf("http://127.0.0.1:%d/vnc.html?autoconnect=1", sess.VNCPort)
		}
		return text(fmt.Sprintf("会话已就绪：%s（应用 %s @ %s，调试端口 %d）",
			sess.ID, sess.App, sess.Ref, sess.CDPPort)), result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "end_session",
		Description: "结束会话：停录屏、归档录像与截图、销毁容器。" +
			"verdict 填 pass/fail/inconclusive，summary 写一句话结论（会显示在报告顶部）。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args endArgs) (*mcp.CallToolResult, endResult, error) {
		if strings.TrimSpace(args.SessionID) == "" {
			return nil, endResult{}, errors.New("sessionId 不能为空")
		}
		sess, err := s.manager.Stop(ctx, args.SessionID, args.Verdict, args.Summary)
		if err != nil {
			return nil, endResult{}, err
		}
		out := endResult{
			SessionID:     sess.ID,
			Status:        string(sess.Status),
			Verdict:       sess.Verdict,
			ReportURL:     fmt.Sprintf("/#/sessions/%s", sess.ID),
			VideoURL:      fmt.Sprintf("/api/sessions/%s/video", sess.ID),
			DurationMS:    sess.VideoDurationMS,
			ReportSummary: sess.Summary,
		}
		return text(fmt.Sprintf("会话已收尾：%s（%s，录像 %.1fs）。验收报告：%s",
			sess.ID, sess.Status, float64(sess.VideoDurationMS)/1000, out.ReportURL)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sessions",
		Description: "列出最近会话及其状态，用于确认哪些还在运行、哪些已经结束。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listResult, error) {
		sessions, err := s.manager.List(ctx, 20)
		if err != nil {
			return nil, listResult{}, err
		}
		items := make([]listItem, 0, len(sessions))
		var b strings.Builder
		for _, sess := range sessions {
			items = append(items, listItem{
				SessionID: sess.ID, App: sess.App, Ref: sess.Ref,
				Status: string(sess.Status), Verdict: sess.Verdict,
				CreatedAt: sess.CreatedAt.Format(time.RFC3339),
			})
			fmt.Fprintf(&b, "- %s %s @ %s [%s] %s\n", sess.ID, sess.App, sess.Ref, sess.Status, sess.Verdict)
		}
		if b.Len() == 0 {
			b.WriteString("(还没有会话)")
		}
		return text(b.String()), listResult{Sessions: items}, nil
	})
}

type endArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Verdict   string `json:"verdict,omitempty" jsonschema:"pass / fail / inconclusive"`
	Summary   string `json:"summary,omitempty" jsonschema:"一句话结论"`
}

type endResult struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Verdict   string `json:"verdict,omitempty"`
	// ReportURL 验收报告（步骤 + 每步画面 + 断言），提 PR 时贴这个
	ReportURL     string `json:"reportUrl"`
	VideoURL      string `json:"videoUrl"`
	DurationMS    int64  `json:"durationMs"`
	ReportSummary string `json:"summary,omitempty"`
}

type listResult struct {
	Sessions []listItem `json:"sessions"`
}

type listItem struct {
	SessionID string `json:"sessionId"`
	App       string `json:"app"`
	Ref       string `json:"ref"`
	Status    string `json:"status"`
	Verdict   string `json:"verdict,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// ---- 页面操作 -------------------------------------------------------------

type sessionArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
}

// stepArgs 报告层的可选字段。填了报告才读得懂；不填也不影响执行。
type stepArgs struct {
	Intent string `json:"intent,omitempty" jsonschema:"这一步为什么做（一句人话，会写进验收报告）"`
	Expect string `json:"expect,omitempty" jsonschema:"期望看到什么（会写进报告，方便对照实际结果）"`
}

// registerSnapshotTool 单独一个「看一眼」：run 结束会自动带当前界面，
// 但那是顺带给的、不进报告；要把它记成一步证据时，显式调这个。
func registerSnapshotTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "snapshot",
		Description: "看一眼当前界面，并把这一眼记成报告里的一步：返回可操作元素（带引用号）+ 一张画面。" +
			"run 结束会自动带当前界面，所以只有「这一步值得单独留证」时才需要它。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionArgs) (*mcp.CallToolResult, snapshotResult, error) {
		snap, shot, err := s.doSnapshot(ctx, args)
		if err != nil {
			return nil, snapshotResult{}, err
		}
		out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: inspect.Summarize(snap)}}}
		if len(shot) > 0 {
			out.Content = append(out.Content, &mcp.ImageContent{Data: shot, MIMEType: "image/jpeg"})
		}
		return out, snapshotResult{URL: snap.URL, Title: snap.Title, Elements: snap.Elements, Text: snap.Text}, nil
	})
}

func registerRunTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "run",
		Description: "按顺序执行一串动作（steps 数组，1~50 步），只做一件事就传一个只含一个元素的数组。" +
			"返回三样东西：每一步的结果、执行完之后的快照（一帧画面 + 可操作元素表）、以及停在第几步（失败时）。" +
			"这一串中间不回到你这边，所以有时效性要求的连招靠它：拖到第 6 秒立刻悬停/断言，不会隔着你思考的那几十秒。" +
			"动作：click 点元素（ref 或 match 文字或 selector）/ " +
			"hover 把鼠标停在元素上（悬停才出现的浮层，如词典）/ fill 覆盖填写输入框 / " +
			"press 按键（组合键也行）/ type 往当前焦点打字 / eval 在页面求值 / wait 等条件成立 / assert 记一条断言。" +
			"press 和 type 走真实键盘输入，页面和原生对话框都收得到。" +
			"建议最后一步用 wait 收尾：界面要加载、要动画的时候，动作刚发出去界面还没稳定，" +
			"这时立刻返回的画面会是中间状态，读者看到的就不是这一步的结果。" +
			"任何一步失败就停，并告诉你跑到第几步、后面还剩几步。" +
			"每一步都会在验收报告里各记一条，当作独立的一步来读。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, runResult, error) {
		msg, res, err := s.doRun(ctx, args)
		if err != nil {
			return nil, runResult{}, err
		}
		// 跑完的界面：先给图（模型看状态靠它），再给需要时截的图
		out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
		if len(res.After) > 0 {
			out.Content = append(out.Content, &mcp.ImageContent{Data: res.After, MIMEType: "image/jpeg"})
		}
		// 把画面带上（最多 3 张）：验证 UI 不能只看元素树——布局错位、空白页、
		// 样式崩了，都只有画面能看出来。snapshot 自带一帧，截图动作带它截的那张。
		for _, r := range res.Steps {
			if len(out.Content) >= 4 {
				break
			}
			switch {
			case len(r.Image) > 0:
				out.Content = append(out.Content, &mcp.ImageContent{Data: r.Image, MIMEType: "image/jpeg"})
			case r.Action == "screenshot":
				if path, ok := r.Value.(string); ok && path != "" {
					if raw, err := os.ReadFile(filepath.Join(mustSessionDir(ctx, s, args.SessionID), path)); err == nil {
						out.Content = append(out.Content, &mcp.ImageContent{Data: raw, MIMEType: "image/png"})
					}
				}
			}
		}
		return out, res, nil
	})
}

func mustSessionDir(ctx context.Context, s *Server, id string) string {
	dir, err := s.sessionDir(ctx, id)
	if err != nil {
		return ""
	}
	return dir
}

type waitArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	For       string `json:"for" jsonschema:"等它成立的 JS 条件表达式"`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"最长等多久（毫秒），默认 5000，最大 60000"`
}

type runArgs struct {
	SessionID string      `json:"sessionId" jsonschema:"会话 ID"`
	Steps     []batchStep `json:"steps" jsonschema:"按顺序执行的步骤（1~50 步）；只做一个动作就传一个元素的数组"`
}

// runResult 给客户端的结构化结果：每一步的成败、元素表、求值结果。
type runResult struct {
	OK        bool            `json:"ok"`
	Steps     []runStepResult `json:"steps"`
	StoppedAt int             `json:"stoppedAt,omitempty"`
	// Elements 跑完之后界面上的可操作元素（模型接着点要用）
	Elements []inspect.Element `json:"elements,omitempty"`
	// After 跑完之后的一帧画面（不进 JSON）
	After []byte `json:"-"`
}

type runStepResult struct {
	Index    int               `json:"index"`
	Action   string            `json:"action"`
	OK       bool              `json:"ok"`
	Text     string            `json:"text"`
	Elements []inspect.Element `json:"elements,omitempty"`
	Value    any               `json:"value,omitempty"`
	// Image 随结果带回给模型的画面（不进 JSON，避免 base64 塞进结构化结果）
	Image []byte `json:"-"`
}

type feedbackArgs struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"关联的会话 ID（可空）"`
	Rating    int    `json:"rating,omitempty" jsonschema:"这套工作流用起来的感受，1（难用）~5（好用），可空"`
	Text      string `json:"text" jsonschema:"具体建议：哪里别扭、缺什么、希望怎么改"`
}

type clickArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	Ref      string  `json:"ref,omitempty" jsonschema:"元素引用号，如 e3（snapshot 给的）"`
	Match    string  `json:"match,omitempty" jsonschema:"按可见文字找元素（如「Skip」），页面刚变、还没有引用号时用它"`
	Selector string  `json:"selector,omitempty" jsonschema:"按 CSS 选择器找元素"`
	X        float64 `json:"x,omitempty" jsonschema:"页面坐标 X（兜底方案）"`
	Y        float64 `json:"y,omitempty" jsonschema:"页面坐标 Y（兜底方案）"`
}

type fillArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	Ref      string `json:"ref,omitempty" jsonschema:"元素引用号"`
	Match    string `json:"match,omitempty" jsonschema:"按可见文字找输入框"`
	Selector string `json:"selector,omitempty" jsonschema:"按 CSS 选择器找输入框"`
	Text     string `json:"text" jsonschema:"要输入的文字"`
}

type pressArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	Key string `json:"key" jsonschema:"按键名"`
}

type evalArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	Script string `json:"script" jsonschema:"JS 表达式"`
}

type assertArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Claim     string `json:"claim" jsonschema:"断言内容：一句能被验证的话，例如「字幕在 12.5s 出现」"`
	OK        bool   `json:"ok" jsonschema:"断言是否成立"`
	Evidence  string `json:"evidence,omitempty" jsonschema:"证据：DOM 片段、数值、日志行。会原样进报告，供人复核"`
}

type actionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type snapshotResult struct {
	URL      string            `json:"url"`
	Title    string            `json:"title"`
	Elements []inspect.Element `json:"elements"`
	Text     string            `json:"text"`
}

type evalResult struct {
	// 用 any 而不是 json.RawMessage：后者会被 SDK 推成"只允许 null/array"的输出 schema，
	// 而页面断言最常返回的是对象（{t:..., paused:...}），会被校验直接拒掉。
	Value any `json:"value"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func sanitizeLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "shot"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	out := replacer.Replace(label)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (s *Server) sessionDir(ctx context.Context, id string) (string, error) {
	sess, err := s.manager.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if sess.Dir == "" {
		return "", fmt.Errorf("会话 %s 没有目录", id)
	}
	return sess.Dir, nil
}

// 编译期断言：确保 store 的终态语义被引用（避免误删依赖）。
var _ = store.StatusFinished
