package mcpserver

// actions.go：动作的实现体 + 「批」。
//
// 每个动作只有一份实现：顶层工具和 batch 都走这里。
// 这样批里的第 3 步和第 3 次单独调用是同一段代码、同一条记录路径，
// 报告里看不出区别，也不会出现"批里能做的事单独调做不了"这种分叉。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// doSnapshot 读页面结构。
func (s *Server) doSnapshot(ctx context.Context, a sessionArgs) (inspect.Snapshot, error) {
	var snap inspect.Snapshot
	_, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "snapshot", Args: map[string]any{}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		got, err := insp.Snapshot(ctx)
		if err != nil {
			return "", "", err
		}
		snap = got
		return inspect.Summarize(got), "", nil
	})
	if err != nil {
		return inspect.Snapshot{}, err
	}
	return snap, nil
}

// doScreenshot 截一张证据图，返回相对路径与字节数。
func (s *Server) doScreenshot(ctx context.Context, a screenshotArgs) (string, int64, error) {
	rel := filepath.Join("screenshots", fmt.Sprintf("%s-%s.png", time.Now().Format("150405"), sanitizeLabel(a.Label)))
	var size int64
	_, artifact, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "screenshot", Args: map[string]any{"label": a.Label}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		dir, err := s.sessionDir(ctx, a.SessionID)
		if err != nil {
			return "", "", err
		}
		n, err := insp.Screenshot(ctx, filepath.Join(dir, rel))
		if err != nil {
			return "", "", err
		}
		size = n
		// 报告里只写"截了什么"，不写落在哪个文件——路径对读者没用。
		return fmt.Sprintf("已截图：%s", a.Label), rel, nil
	})
	if err != nil {
		return "", 0, err
	}
	return artifact, size, nil
}

// locator 能"现找元素"的检查器（假实现不实现，按能力探测）。
type locator interface {
	Locate(ctx context.Context, match, selector string) (string, error)
}

// resolveRef 把「引用号 / 可见文字 / CSS 选择器」统一成一个引用号。
//
// 批里必须支持后两种：引用号是某次 snapshot 的产物，页面一翻页就作废，
// 而连招里要点的按钮往往在这一步才出现。
func (s *Server) resolveRef(ctx context.Context, sid, ref, match, selector string) (string, error) {
	if ref != "" || (match == "" && selector == "") {
		return ref, nil
	}
	insp, err := s.manager.Inspector(ctx, sid)
	if err != nil {
		return "", err
	}
	l, ok := insp.(locator)
	if !ok {
		return "", errors.New("当前检查器不支持按文字/选择器定位")
	}
	return l.Locate(ctx, match, selector)
}

// doClick 点一个元素（引用号优先，可用可见文字/选择器，坐标兜底）。
func (s *Server) doClick(ctx context.Context, a clickArgs) (string, error) {
	if a.Ref == "" && a.Match == "" && a.Selector == "" && a.X == 0 && a.Y == 0 {
		return "", errors.New("ref、match/selector、x/y 至少要给一个")
	}
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "click", Args: map[string]any{"ref": a.Ref, "x": a.X, "y": a.Y},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if a.Ref != "" {
			if err := insp.Click(ctx, a.Ref); err != nil {
				return "", "", err
			}
			// 报告里写元素的名字，不写引用号：读者不认识 q1/e5
			if el, ok := s.manager.Describe(insp, a.Ref); ok && el.Name != "" {
				return "已点击「" + el.Name + "」", "", nil
			}
			return "已点击 " + a.Ref, "", nil
		}
		if err := insp.ClickAt(ctx, a.X, a.Y); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("已点击坐标 (%.0f, %.0f)", a.X, a.Y), "", nil
	})
	return msg, err
}

// doFill 覆盖填写文本，可选回车提交。
func (s *Server) doFill(ctx context.Context, a fillArgs) (string, error) {
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "fill", Args: map[string]any{"ref": a.Ref, "text": a.Text, "submit": a.Submit},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if err := insp.Fill(ctx, a.Ref, a.Text); err != nil {
			return "", "", err
		}
		if a.Submit {
			if err := insp.Press(ctx, "Enter"); err != nil {
				return "", "", err
			}
		}
		return fmt.Sprintf("已填入 %q", a.Text), "", nil
	})
	return msg, err
}

// doPress 给当前焦点发一次按键。
func (s *Server) doPress(ctx context.Context, a pressArgs) (string, error) {
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "press_key", Args: map[string]any{"key": a.Key}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if err := insp.Press(ctx, a.Key); err != nil {
			return "", "", err
		}
		return "已按键 " + a.Key, "", nil
	})
	return msg, err
}

// doEval 在页面里求值。
func (s *Server) doEval(ctx context.Context, a evalArgs) (string, any, error) {
	var out any
	_, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "eval", Args: map[string]any{"script": a.Script}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		raw, err := insp.Evaluate(ctx, a.Script)
		if err != nil {
			return "", "", err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			out = string(raw)
		}
		return string(raw), "", nil
	})
	if err != nil {
		return "", nil, err
	}
	pretty, _ := json.Marshal(out)
	return string(pretty), out, nil
}

// doWait 等服务端条件成立：每 100ms 求值一次，超时就失败。
//
// 为什么放在服务端等：等界面稳定这件事一旦交给模型，中间就是几十秒的思考，
// 等到再来截图时画面早就不是那一刻了。
func (s *Server) doWait(ctx context.Context, a waitArgs) (string, error) {
	expr := strings.TrimSpace(a.For)
	if expr == "" {
		return "", errors.New("for 不能为空：写一个等它成立的 JS 条件")
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}

	var lastErr error
	result, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "wait", Args: map[string]any{"for": expr, "timeoutMs": timeout.Milliseconds()},
		Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		started := time.Now()
		for {
			raw, evalErr := insp.Evaluate(ctx, "Boolean("+expr+")")
			if evalErr != nil {
				// 元素还没出现时求值会抛错，这本身就是"还没到"，继续等
				lastErr = evalErr
			} else if strings.TrimSpace(string(raw)) == "true" {
				return fmt.Sprintf("条件成立（等了 %.1f 秒）", time.Since(started).Seconds()), "", nil
			}
			if time.Since(started) >= timeout {
				msg := fmt.Sprintf("等待超时（%s）：%s", timeout, expr)
				if lastErr != nil {
					msg += "；最后一次求值错误：" + lastErr.Error()
				}
				return "", "", errors.New(msg)
			}
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
	return result, err
}

// doAssert 记一条断言。
func (s *Server) doAssert(ctx context.Context, a assertArgs) error {
	if strings.TrimSpace(a.Claim) == "" {
		return errors.New("claim 不能为空")
	}
	return s.manager.Assert(ctx, a.SessionID, a.Claim, a.OK, a.Evidence)
}

// ---- 批 -------------------------------------------------------------------

const (
	maxBatchSteps = 50
	batchTimeout  = 2 * time.Minute
)

// batchStep 批里的一步：动作名 + 该动作自己的参数。
//
// 故意做成扁平结构而不是嵌套 JSON：模型写错一层就整个批跑不了，
// 扁平至少能一眼看出哪一项写错了。
type batchStep struct {
	Action string `json:"action" jsonschema:"动作：snapshot/click/fill/press_key/eval/wait/screenshot/assert"`
	stepArgs

	Ref       string  `json:"ref,omitempty"`
	Match     string  `json:"match,omitempty"`
	Selector  string  `json:"selector,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Text      string  `json:"text,omitempty"`
	Submit    bool    `json:"submit,omitempty"`
	Key       string  `json:"key,omitempty"`
	Script    string  `json:"script,omitempty"`
	For       string  `json:"for,omitempty"`
	TimeoutMS int     `json:"timeoutMs,omitempty"`
	Label     string  `json:"label,omitempty"`
	Claim     string  `json:"claim,omitempty"`
	OK        *bool   `json:"ok,omitempty"`
	Evidence  string  `json:"evidence,omitempty"`
}

// stepExtra 是某些动作额外要交给模型的东西（元素表、求值结果、要展示的原文）。
type stepExtra struct {
	Text     string
	Elements []inspect.Element
	Value    any
}

// runBatchStep 执行一步；返回一句给人看的结果 + 给模型的额外载荷。
func (s *Server) runBatchStep(ctx context.Context, sid string, st batchStep) (string, stepExtra, error) {
	base := stepArgs{Intent: st.Intent, Expect: st.Expect}
	switch st.Action {
	case "snapshot":
		snap, err := s.doSnapshot(ctx, sessionArgs{SessionID: sid, stepArgs: base})
		if err != nil {
			return "", stepExtra{}, err
		}
		return fmt.Sprintf("读到 %d 个可操作元素", len(snap.Elements)), stepExtra{
			Text: inspect.Summarize(snap), Elements: snap.Elements,
		}, nil
	case "click":
		msg, err := s.doClick(ctx, clickArgs{SessionID: sid, stepArgs: base, Ref: st.Ref, Match: st.Match, Selector: st.Selector, X: st.X, Y: st.Y})
		return msg, stepExtra{}, err
	case "fill":
		msg, err := s.doFill(ctx, fillArgs{SessionID: sid, stepArgs: base, Ref: st.Ref, Match: st.Match, Selector: st.Selector, Text: st.Text, Submit: st.Submit})
		return msg, stepExtra{}, err
	case "press_key":
		msg, err := s.doPress(ctx, pressArgs{SessionID: sid, stepArgs: base, Key: st.Key})
		return msg, stepExtra{}, err
	case "eval":
		pretty, val, err := s.doEval(ctx, evalArgs{SessionID: sid, stepArgs: base, Script: st.Script})
		if err != nil {
			return "", stepExtra{}, err
		}
		return pretty, stepExtra{Value: val}, nil
	case "wait":
		msg, err := s.doWait(ctx, waitArgs{SessionID: sid, stepArgs: base, For: st.For, TimeoutMS: st.TimeoutMS})
		return msg, stepExtra{}, err
	case "screenshot":
		path, size, err := s.doScreenshot(ctx, screenshotArgs{SessionID: sid, stepArgs: base, Label: st.Label})
		if err != nil {
			return "", stepExtra{}, err
		}
		return fmt.Sprintf("已截图：%s（%d 字节）", st.Label, size), stepExtra{Value: path}, nil
	case "assert":
		if st.OK == nil {
			return "", stepExtra{}, errors.New("assert 必须给 ok")
		}
		if err := s.doAssert(ctx, assertArgs{SessionID: sid, Claim: st.Claim, OK: *st.OK, Evidence: st.Evidence}); err != nil {
			return "", stepExtra{}, err
		}
		if *st.OK {
			return "断言已记录 ✅：" + st.Claim, stepExtra{}, nil
		}
		return "断言已记录 ❌：" + st.Claim, stepExtra{}, nil
	default:
		return "", stepExtra{}, fmt.Errorf("未知动作 %q（可用：snapshot/click/fill/press_key/eval/wait/screenshot/assert）", st.Action)
	}
}

// doBatch 连续执行若干步；任何一步失败就停下，并把进度说清楚。
//
// 失败不返回 Go error：模型需要知道"跑到第几步、剩下没跑什么"，
// 用错误把它吞掉反而更糊涂。会话本身的状态在报告里，不靠这里的返回值。
func (s *Server) doBatch(ctx context.Context, a batchArgs) (string, batchResult, error) {
	if len(a.Steps) == 0 {
		return "", batchResult{}, errors.New("steps 不能为空")
	}
	if len(a.Steps) > maxBatchSteps {
		return "", batchResult{}, fmt.Errorf("一次最多 %d 步（收到 %d 步）", maxBatchSteps, len(a.Steps))
	}

	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	var b strings.Builder
	res := batchResult{Steps: make([]batchStepResult, 0, len(a.Steps))}
	for i, st := range a.Steps {
		msg, extra, err := s.runBatchStep(ctx, a.SessionID, st)
		if err != nil {
			fmt.Fprintf(&b, "%d. %s ✗ %v\n", i+1, st.Action, err)
			fmt.Fprintf(&b, "已停止：后面 %d 步没有执行。", len(a.Steps)-i-1)
			res.Steps = append(res.Steps, batchStepResult{
				Index: i + 1, Action: st.Action, OK: false, Text: err.Error(), Elements: extra.Elements, Value: extra.Value,
			})
			res.StoppedAt = i + 1
			return b.String(), res, nil
		}
		fmt.Fprintf(&b, "%d. %s ✓ %s\n", i+1, st.Action, msg)
		if extra.Text != "" {
			// 读页面结构这类动作，结果本身就是模型下一步的依据，原样贴出来
			b.WriteString(extra.Text)
			b.WriteString("\n")
		}
		res.Steps = append(res.Steps, batchStepResult{
			Index: i + 1, Action: st.Action, OK: true, Text: msg, Elements: extra.Elements, Value: extra.Value,
		})
	}
	if len(a.Steps) == 1 {
		fmt.Fprintf(&b, "完成。")
	} else {
		fmt.Fprintf(&b, "全部 %d 步执行完成。", len(a.Steps))
	}
	res.OK = true
	return b.String(), res, nil
}

func registerFeedbackTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "feedback",
		Description: "用完这套工作流后，把「哪里别扭、缺什么、希望怎么改」写下来。" +
			"这不是验收结论，也不会进报告——它只用于产品改进：被人看过之后统一变成改进项。" +
			"用起来不顺、某个动作每次都要绕、报告哪里看不懂，都可以写。rating 可选（1~5）。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args feedbackArgs) (*mcp.CallToolResult, actionResult, error) {
		id, err := s.doFeedback(ctx, args)
		if err != nil {
			return nil, actionResult{}, err
		}
		return text(fmt.Sprintf("建议已记录（#%d）。", id)), actionResult{OK: true, Message: "建议已记录"}, nil
	})
}

// doFeedback 记一条对工作流本身的建议。
//
// 它不是验收证据，也不进报告：这是给产品改进用的输入。
// 单独列出来，是为了让模型知道"觉得哪里别扭"是有地方说的，
// 而不是在报告里写一句"这个工具不好用"就算完。
func (s *Server) doFeedback(ctx context.Context, a feedbackArgs) (int64, error) {
	body := strings.TrimSpace(a.Text)
	if body == "" {
		return 0, errors.New("text 不能为空：具体说哪里别扭、缺什么")
	}
	if a.Rating < 0 || a.Rating > 5 {
		return 0, errors.New("rating 只能是 1~5（可空）")
	}
	return s.store.AddFeedback(ctx, store.Feedback{
		At:        time.Now(),
		SessionID: a.SessionID,
		Source:    "ai",
		Rating:    a.Rating,
		Body:      body,
	})
}
