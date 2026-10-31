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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// Server 包装 SDK server 与调度器。
type Server struct {
	manager *session.Manager
	store   *store.Store
	log     *slog.Logger
	// cfg 只用来读超时（批上限、单步上限），其余配置不在这里用。
	cfg *config.Config
}

// NewServer 注册全部工具并返回可直接 Run 的 MCP server。
func NewServer(mgr *session.Manager, st *store.Store, cfg *config.Config, log *slog.Logger) *mcp.Server {
	s := &Server{manager: mgr, store: st, log: log, cfg: cfg}
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "amber",
		Title:       "Amber",
		Description: "一台给 AI 用的一次性 GUI 机器：起容器、驱动应用、全程录屏、留下可回看的记录",
		Version:     "0.1.0",
	}, nil)

	registerSessionTools(server, s)
	registerCaptureTool(server, s)
	registerActTool(server, s)
	registerFeedbackTool(server, s)

	return server
}

// ---- 会话管理 -------------------------------------------------------------

type startArgs struct {
	// Payload 被测产物：同机用 dir:/绝对路径；跨机先 POST /api/blobs + /api/manifests，再传 manifest:sha256:…
	Payload string `json:"payload" jsonschema:"被测产物：dir:/绝对路径 或 manifest:sha256:…（必填）"`
	// Assets 产物 rig.json 里声明的素材槽位 → 来源
	Assets map[string]string `json:"assets,omitempty" jsonschema:"素材槽位映射，槽位由产物 rig.json 声明"`
	// Title 这次验收的意图：列表和记录顶部先展示它，读者不必靠会话 ID 认人
	Title  string `json:"title" jsonschema:"这次验收要干什么（一句话，如「检查播放页字幕与词典」）；会当会话标题展示"`
	Label  string `json:"label,omitempty" jsonschema:"展示名；留空用产物 rig.json 里的 name"`
	Commit string `json:"commit,omitempty" jsonschema:"可选的来源版本号，只用于记录溯源"`
}

type startResult struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	CDPPort   int    `json:"cdpPort"`
	VNCURL    string `json:"vncUrl,omitempty"`
}

func registerSessionTools(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "amber_start",
		Description: "申请一台一次性机器：拉起容器、启动被测应用、开始全程录屏。" +
			"返回 sessionId，之后所有操作都要带上它。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args startArgs) (*mcp.CallToolResult, startResult, error) {
		if strings.TrimSpace(args.Payload) == "" {
			return nil, startResult{}, errors.New("payload 不能为空（dir:/绝对路径 或 manifest:sha256:…）")
		}
		// 标题是读者认这一条会话的唯一线索，不许空着编一个默认值。
		if strings.TrimSpace(args.Title) == "" {
			return nil, startResult{}, errors.New("title 不能为空：一句话说清这次验收要干什么（会当会话标题）")
		}
		sess, err := s.manager.Start(ctx, session.Options{
			Label: args.Label, Title: args.Title, Payload: args.Payload, Assets: args.Assets, Commit: args.Commit,
		})
		if err != nil {
			return nil, startResult{}, err
		}
		result := startResult{
			SessionID: sess.ID,
			Status:    string(sess.Status),
			CDPPort:   sess.CDPPort,
		}
		if sess.VNCPort > 0 {
			result.VNCURL = fmt.Sprintf("http://127.0.0.1:%d/vnc.html?autoconnect=1", sess.VNCPort)
		}
		return text(fmt.Sprintf("会话已就绪：%s（应用 %s @ %s，调试端口 %d）",
			sess.ID, sess.App, sess.Ref, sess.CDPPort)), result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "amber_stop",
		Description: "结束会话：停录屏、归档每批的画面片段与截图、销毁容器。" +
			"verdict 填 pass/fail/inconclusive，summary 写一句话结论（会显示在记录顶部）。",
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
			DurationMS:    sess.VideoDurationMS,
			ReportSummary: sess.Summary,
		}
		return text(fmt.Sprintf("会话已收尾：%s（%s，录像 %.1fs）。记录：%s",
			sess.ID, sess.Status, float64(sess.VideoDurationMS)/1000, out.ReportURL)), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "amber_list",
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
	// ReportURL 记录（步骤 + 每步画面 + 断言），提 PR 时贴这个
	ReportURL     string `json:"reportUrl"`
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

// stepArgs 记录层的可选字段。填了记录才读得懂；不填也不影响执行。
type stepArgs struct {
	Intent string `json:"intent,omitempty" jsonschema:"这一步为什么做（一句人话，会写进记录）"`
	Expect string `json:"expect,omitempty" jsonschema:"期望看到什么（会写进记录，方便对照实际结果）"`
}

// registerCaptureTool 单独一个「看一眼」：run 收尾也会 capture，
// 但那是这一批的最后一步；要单独留证时显式调这个。
//
// 参数和 run 里的 capture 完全一致——同一个动作只有一份定义，不做成两套。
func registerCaptureTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "amber_capture",
		Description: "看一眼当前界面，并把这一眼记成记录里的一步：" +
			"返回可操作元素（带引用号）+ 一张整屏画面。" +
			"画面是整块屏幕，不是网页截图——原生对话框、系统菜单也在里面。" +
			"可选 for（先等这个 JS 条件成立再拍，最多等 timeoutMs）；等不到也会把当时的画面带回来。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args captureArgs) (*mcp.CallToolResult, captureToolResult, error) {
		snap, result, shot, err := s.doCapture(ctx, args)
		if err != nil {
			return nil, captureToolResult{}, err
		}
		out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result}}}
		if len(shot) > 0 {
			out.Content = append(out.Content, &mcp.ImageContent{Data: shot, MIMEType: "image/jpeg"})
		}
		return out, captureToolResult{URL: snap.URL, Title: snap.Title, Elements: snap.Elements, Text: snap.Text}, nil
	})
}

func registerActTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "amber_run",
		Description: "执行一串动作（steps 数组，1~50 步），并返回执行完之后的界面；只做一件事就传一个只含一个元素的数组。" +
			"intent 必填：一句话说清这一段要干什么，记录里它就是这一段的小标题（不写就没有标题，读者只能看一串点击）。" +
			"返回三样东西：每一步的结果、执行完之后的界面（一帧整屏画面 + 可操作元素表）、以及停在第几步（失败或超时时）。" +
			"\n\n" +
			"动作与必填字段（每个动作只管一件事，别混用）：\n" +
			"  click  ref 或 match 或 selector 或 x+y —— 点元素\n" +
			"  hover  同上 —— 把鼠标停在元素上（悬停才出现的浮层，如词典）\n" +
			"  fill   ref/match/selector + text —— 覆盖填写输入框\n" +
			"  press  key —— 按键，组合键写成 ctrl+l；页面和原生对话框都收得到\n" +
			"  type   text —— 往当前焦点打字\n" +
			"  eval   script —— 在页面里求值，返回值原样给你\n" +
			"  wait   for（+timeoutMs，默认 5000）—— 等条件成立，等不到算这一步失败\n" +
			"  sleep  ms（1~60000）—— 等固定时长，不会失败\n" +
			"  capture 可选 for（+timeoutMs，默认 3000）—— 看一眼并留证，必须是最后一步\n" +
			"\n" +
			"最后一步必须是 capture：等到界面稳定再把画面带回来，别拿动画中间帧当结果。" +
			"这一串中间不回到你这边，所以有时效性要求的连招靠它：拖到第 6 秒立刻悬停，不会隔着你思考的那几十秒。" +
			"任何一步失败就停，并告诉你跑到第几步、后面还剩几步；整批超时也一样，已完成的步骤照样带回来。" +
			"每一步都会在记录里各记一条，当作独立的一步来读。",
		InputSchema: runInputSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args actArgs) (*mcp.CallToolResult, actResult, error) {
		msg, res, err := s.doAct(ctx, args)
		if err != nil {
			return nil, actResult{}, err
		}
		// 跑完的界面：先给图（模型看状态靠它），再给中途 capture 拍到的图
		out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
		if len(res.Shot) > 0 {
			out.Content = append(out.Content, &mcp.ImageContent{Data: res.Shot, MIMEType: "image/jpeg"})
		}
		for _, r := range res.Steps {
			if len(out.Content) >= 4 {
				break
			}
			if len(r.Image) > 0 {
				out.Content = append(out.Content, &mcp.ImageContent{Data: r.Image, MIMEType: "image/jpeg"})
			}
		}
		return out, res, nil
	})
}

// runInputSchema 在推断出来的 schema 上补两样东西：action 的取值清单、
// 以及每个字段「哪个动作用它」的说明。
//
// 只写散文是这次踩到的坑：调用方看到 "assert" 却找不到实现，看到 ref/match/x/y
// 全 optional 只能猜。枚举交给 schema 校验，写错立刻被拒，不跑到一半才说。
func runInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[actArgs](nil)
	if err != nil {
		panic(fmt.Sprintf("推断 run 的输入 schema 失败: %v", err))
	}
	steps, ok := schema.Properties["steps"]
	if !ok || steps.Items == nil {
		panic("run 的输入 schema 缺少 steps")
	}
	if action, ok := steps.Items.Properties["action"]; ok {
		action.Enum = actionNames()
		action.Description = "动作：见工具说明的对照表（" + actionHelp() + "）"
	}
	return schema
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

type sleepArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	MS int `json:"ms" jsonschema:"等多少毫秒（1~60000）；等条件成立请用 wait"`
}

// captureArgs 一次「看一眼」的参数。工具 capture 与 run 里的 capture 共用这一份定义。
type captureArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	stepArgs
	For       string `json:"for,omitempty" jsonschema:"先等它成立的 JS 条件；可以不填，纯拍当前画面"`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"最多等多久（毫秒），默认 3000，最大 60000"`
}

type actArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	// Intent 这一批的意图：记录里它是这一段的小标题，读者看这一句就够
	Intent string      `json:"intent" jsonschema:"这一批要干什么（一句话，如「走完引导进入首页」）；记录里当这一段标题"`
	Steps  []batchStep `json:"steps" jsonschema:"按顺序执行的步骤（1~50 步）；只做一个动作就传一个元素的数组"`
}

// actResult 给客户端的结构化结果：每一步的成败、元素表、求值结果。
type actResult struct {
	OK        bool            `json:"ok"`
	Steps     []actStepResult `json:"steps"`
	StoppedAt int             `json:"stoppedAt,omitempty"`
	// Elements 跑完之后界面上的可操作元素（模型接着点要用）
	Elements []inspect.Element `json:"elements,omitempty"`
	// Shot 跑完之后的一帧整屏画面（不进 JSON）
	Shot []byte `json:"-"`
}

type actStepResult struct {
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

type actionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// captureToolResult 和 run 的收尾给同一份东西：界面地址、标题、可操作元素、可见文字。
type captureToolResult struct {
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
