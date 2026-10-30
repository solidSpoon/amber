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
	"encoding/json"
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
	log     *slog.Logger
}

// NewServer 注册全部工具并返回可直接 Run 的 MCP server。
func NewServer(mgr *session.Manager, log *slog.Logger) *mcp.Server {
	s := &Server{manager: mgr, log: log}
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "rigd",
		Title:       "Acceptance Rig",
		Description: "一次性 GUI 验收环境：起容器、驱动应用、全程录屏、留审计",
		Version:     "0.1.0",
	}, nil)

	registerSessionTools(server, s)
	registerActionTools(server, s)
	return server
}

// ---- 会话管理 -------------------------------------------------------------

type startArgs struct {
	App string `json:"app" jsonschema:"被测应用标识，例如 DashPlayer"`
	Ref string `json:"ref,omitempty" jsonschema:"代码版本：分支名或 commit，默认 main"`
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
		Description: "申请一台一次性测试机：拉起容器、构建并启动被测应用、开始全程录屏。" +
			"返回 sessionId，之后所有操作都要带上它。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args startArgs) (*mcp.CallToolResult, startResult, error) {
		if strings.TrimSpace(args.App) == "" {
			return nil, startResult{}, errors.New("app 不能为空")
		}
		if args.Ref == "" {
			args.Ref = "main"
		}
		sess, err := s.manager.Start(ctx, session.Options{App: args.App, Ref: args.Ref})
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
		Description: "结束会话：停止录屏、归档录像与截图、销毁容器。" +
			"verdict 填 pass/fail/inconclusive，findings 写清依据，报告会带上它们。",
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
			VideoURL:      fmt.Sprintf("/api/sessions/%s/video", sess.ID),
			DurationMS:    sess.VideoDurationMS,
			ReportSummary: sess.Summary,
		}
		return text(fmt.Sprintf("会话已收尾：%s（%s，录像 %.1fs）",
			sess.ID, sess.Status, float64(sess.VideoDurationMS)/1000)), out, nil
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
	SessionID     string `json:"sessionId"`
	Status        string `json:"status"`
	Verdict       string `json:"verdict,omitempty"`
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
}

func registerActionTools(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "snapshot",
		Description: "读当前页面结构：可交互元素（带引用号 e1/e2…、角色、可读名称、位置）与可见文本。" +
			"先用它确定目标，再用引用号点击，不要靠猜坐标。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args sessionArgs) (*mcp.CallToolResult, snapshotResult, error) {
		var snap inspect.Snapshot
		_, _, err := s.manager.Record(ctx, args.SessionID, "snapshot", map[string]any{}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
			got, err := insp.Snapshot(ctx)
			if err != nil {
				return "", "", err
			}
			snap = got
			return inspect.Summarize(got), "", nil
		})
		if err != nil {
			return nil, snapshotResult{}, err
		}
		return text(inspect.Summarize(snap)), snapshotResult{
			URL: snap.URL, Title: snap.Title, Elements: snap.Elements, Text: snap.Text,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "screenshot",
		Description: "截图作为证据，返回图片本身（同时落盘）。label 写清用途，比如「第3条字幕」。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args screenshotArgs) (*mcp.CallToolResult, screenshotResult, error) {
		name := sanitizeLabel(args.Label)
		rel := filepath.Join("screenshots", fmt.Sprintf("%s-%s.png", time.Now().Format("150405"), name))

		var path string
		var size int64
		_, artifact, err := s.manager.Record(ctx, args.SessionID, "screenshot", map[string]any{"label": args.Label},
			func(ctx context.Context, insp session.Inspector) (string, string, error) {
				dir, err := s.sessionDir(ctx, args.SessionID)
				if err != nil {
					return "", "", err
				}
				path = filepath.Join(dir, rel)
				n, err := insp.Screenshot(ctx, path)
				if err != nil {
					return "", "", err
				}
				size = n
				return fmt.Sprintf("截图已保存：%s（%d 字节）", rel, n), rel, nil
			})
		if err != nil {
			return nil, screenshotResult{}, err
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, screenshotResult{}, fmt.Errorf("读取截图失败: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("截图已保存：%s", artifact)},
				&mcp.ImageContent{Data: raw, MIMEType: "image/png"},
			},
		}, screenshotResult{Path: artifact, Bytes: size}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "click",
		Description: "点击元素：ref 用 snapshot 给出的 eN；也可以给 x/y 走坐标兜底（原生弹窗、自绘控件）。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args clickArgs) (*mcp.CallToolResult, actionResult, error) {
		if args.Ref == "" && (args.X == 0 && args.Y == 0) {
			return nil, actionResult{}, errors.New("ref 与 x/y 至少要给一个")
		}
		msg, _, err := s.manager.Record(ctx, args.SessionID, "click",
			map[string]any{"ref": args.Ref, "x": args.X, "y": args.Y},
			func(ctx context.Context, insp session.Inspector) (string, string, error) {
				if args.Ref != "" {
					if err := insp.Click(ctx, args.Ref); err != nil {
						return "", "", err
					}
					return "已点击 " + args.Ref, "", nil
				}
				if err := insp.ClickAt(ctx, args.X, args.Y); err != nil {
					return "", "", err
				}
				return fmt.Sprintf("已点击坐标 (%.0f, %.0f)", args.X, args.Y), "", nil
			})
		if err != nil {
			return nil, actionResult{}, err
		}
		return text(msg), actionResult{OK: true, Message: msg}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fill",
		Description: "往输入框填文本（覆盖原内容）。submit=true 时按回车提交。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args fillArgs) (*mcp.CallToolResult, actionResult, error) {
		msg, _, err := s.manager.Record(ctx, args.SessionID, "fill",
			map[string]any{"ref": args.Ref, "text": args.Text, "submit": args.Submit},
			func(ctx context.Context, insp session.Inspector) (string, string, error) {
				if err := insp.Fill(ctx, args.Ref, args.Text); err != nil {
					return "", "", err
				}
				if args.Submit {
					if err := insp.Press(ctx, "Enter"); err != nil {
						return "", "", err
					}
				}
				return fmt.Sprintf("已填入 %q", args.Text), "", nil
			})
		if err != nil {
			return nil, actionResult{}, err
		}
		return text(msg), actionResult{OK: true, Message: msg}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "press_key",
		Description: "给当前焦点发按键：Enter / Escape / Space / ArrowRight / ArrowLeft 等。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args pressArgs) (*mcp.CallToolResult, actionResult, error) {
		msg, _, err := s.manager.Record(ctx, args.SessionID, "press_key", map[string]any{"key": args.Key},
			func(ctx context.Context, insp session.Inspector) (string, string, error) {
				if err := insp.Press(ctx, args.Key); err != nil {
					return "", "", err
				}
				return "已按键 " + args.Key, "", nil
			})
		if err != nil {
			return nil, actionResult{}, err
		}
		return text(msg), actionResult{OK: true, Message: msg}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "eval",
		Description: "在页面里执行只读 JS 并返回结果，用于精确断言（如读 video.currentTime）。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args evalArgs) (*mcp.CallToolResult, evalResult, error) {
		var out any
		_, _, err := s.manager.Record(ctx, args.SessionID, "eval", map[string]any{"script": args.Script},
			func(ctx context.Context, insp session.Inspector) (string, string, error) {
				raw, err := insp.Evaluate(ctx, args.Script)
				if err != nil {
					return "", "", err
				}
				// 原样保留 JSON（对象/数组/标量），既给模型看文本也给客户端结构化值
				if err := json.Unmarshal(raw, &out); err != nil {
					out = string(raw)
				}
				return string(raw), "", nil
			})
		if err != nil {
			return nil, evalResult{}, err
		}
		pretty, _ := json.Marshal(out)
		return text(string(pretty)), evalResult{Value: out}, nil
	})
}

type screenshotArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Label     string `json:"label" jsonschema:"本次截图的用途"`
}

type screenshotResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type clickArgs struct {
	SessionID string  `json:"sessionId" jsonschema:"会话 ID"`
	Ref       string  `json:"ref,omitempty" jsonschema:"元素引用号，如 e3"`
	X         float64 `json:"x,omitempty" jsonschema:"页面坐标 X（兜底方案）"`
	Y         float64 `json:"y,omitempty" jsonschema:"页面坐标 Y（兜底方案）"`
}

type fillArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Ref       string `json:"ref" jsonschema:"元素引用号"`
	Text      string `json:"text" jsonschema:"要输入的文字"`
	Submit    bool   `json:"submit,omitempty" jsonschema:"是否回车提交"`
}

type pressArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Key       string `json:"key" jsonschema:"按键名"`
}

type evalArgs struct {
	SessionID string `json:"sessionId" jsonschema:"会话 ID"`
	Script    string `json:"script" jsonschema:"JS 表达式"`
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
