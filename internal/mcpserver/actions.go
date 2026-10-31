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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// doSnapshot 读页面结构。
func (s *Server) doSnapshot(ctx context.Context, a sessionArgs) (inspect.Snapshot, []byte, error) {
	var snap inspect.Snapshot
	var shot []byte
	_, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "snapshot", Args: map[string]any{}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		got, err := insp.Snapshot(ctx)
		if err != nil {
			return "", "", err
		}
		snap = got
		// 顺手取一帧画面：看结构能知道"有什么"，看画面才知道"长什么样"
		if previewer, ok := insp.(interface {
			PreviewJPEG(ctx context.Context, quality int) ([]byte, error)
		}); ok {
			if raw, err := previewer.PreviewJPEG(ctx, 70); err == nil {
				shot = raw
			}
		}
		return inspect.Summarize(got), "", nil
	})
	if err != nil {
		return inspect.Snapshot{}, nil, err
	}
	return snap, shot, nil
}

// locator 能"现找元素"的检查器（测试里的假实现不实现，按能力探测）。
type locator interface {
	Locate(ctx context.Context, match, selector string) (string, error)
}

// resolveRef 把「引用号 / 可见文字 / CSS 选择器」统一成一个引用号。
//
// 批里必须支持后两种：引用号是某次 snapshot 的产物，页面一变就作废，
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

// doHover 把鼠标移到元素上：悬停才出现的浮层（词典释义）用它。
func (s *Server) doHover(ctx context.Context, a clickArgs) (string, error) {
	if a.Ref == "" && a.Match == "" && a.Selector == "" && a.X == 0 && a.Y == 0 {
		return "", errors.New("hover 需要 ref、match/selector 或 x/y")
	}
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "hover", Args: map[string]any{"ref": a.Ref, "x": a.X, "y": a.Y},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if a.Ref != "" {
			if err := insp.Hover(ctx, a.Ref); err != nil {
				return "", "", err
			}
			if el, ok := s.manager.Describe(insp, a.Ref); ok && el.Name != "" {
				return "已把鼠标移到「" + el.Name + "」上", "", nil
			}
			return "已移入 " + a.Ref, "", nil
		}
		if err := insp.HoverAt(ctx, a.X, a.Y); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("已移入坐标 (%.0f, %.0f)", a.X, a.Y), "", nil
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
		Tool: "fill", Args: map[string]any{"ref": a.Ref, "text": a.Text},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if err := insp.Fill(ctx, a.Ref, a.Text); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("已填入 %q", a.Text), "", nil
	})
	return msg, err
}

// doPress 给当前焦点发一次按键（组合键也行，如 ctrl+l）。
//
// 走系统层而不是页面的键盘事件：真实按键页面和原生对话框都收得到，
// 一套动作覆盖两种情况，AI 不用判断"该用哪个按键动作"。
func (s *Server) doPress(ctx context.Context, a pressArgs) (string, error) {
	if strings.TrimSpace(a.Key) == "" {
		return "", errors.New("press 需要 key（如 Enter、ArrowRight、ctrl+l）")
	}
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "press", Args: map[string]any{"key": a.Key}, Intent: a.Intent, Expect: a.Expect,
		Caption: "按下 " + a.Key,
	}, func(ctx context.Context, _ session.Inspector) (string, string, error) {
		if err := s.manager.DesktopKey(ctx, a.SessionID, a.Key); err != nil {
			return "", "", err
		}
		return "已按下 " + a.Key, "", nil
	})
	return msg, err
}

// doTypeText 往当前焦点敲一段字（原生对话框里输路径用它）。
func (s *Server) doTypeText(ctx context.Context, a pressArgs) (string, error) {
	if a.Key == "" {
		return "", errors.New("type 需要 text")
	}
	msg, _, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "type", Args: map[string]any{"text": a.Key}, Intent: a.Intent, Expect: a.Expect,
		Caption: "输入「" + a.Key + "」",
	}, func(ctx context.Context, _ session.Inspector) (string, string, error) {
		if err := s.manager.DesktopType(ctx, a.SessionID, a.Key); err != nil {
			return "", "", err
		}
		return "已输入 " + a.Key, "", nil
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
	maxRunSteps = 50
	runTimeout  = 2 * time.Minute
)

// batchStep 批里的一步：动作名 + 该动作自己的参数。
//
// 故意做成扁平结构而不是嵌套 JSON：模型写错一层就整个批跑不了，
// 扁平至少能一眼看出哪一项写错了。
type batchStep struct {
	Action string `json:"action" jsonschema:"动作：snapshot/click/hover/fill/press/type/eval/wait/assert"`
	stepArgs

	Ref       string  `json:"ref,omitempty"`
	Match     string  `json:"match,omitempty"`
	Selector  string  `json:"selector,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Text      string  `json:"text,omitempty"`
	Key       string  `json:"key,omitempty"`
	Script    string  `json:"script,omitempty"`
	For       string  `json:"for,omitempty"`
	TimeoutMS int     `json:"timeoutMs,omitempty"`
	Label     string  `json:"label,omitempty"`
	Claim     string  `json:"claim,omitempty"`
	OK        *bool   `json:"ok,omitempty"`
	Evidence  string  `json:"evidence,omitempty"`
	Op        string  `json:"op,omitempty"`
	Value     string  `json:"value,omitempty"`
}

// stepExtra 是某些动作额外要交给模型的东西（元素表、求值结果、要展示的原文）。
type stepExtra struct {
	Text     string
	Elements []inspect.Element
	Value    any
	// Image 随结果带回给模型的画面（JPEG）。UI 验收只看元素树是不够的：
	// 布局错位、空白页、样式崩了，都只有画面能看出来。
	Image []byte
}

// runStep 执行一步；返回一句给人看的结果 + 给模型的额外载荷。
//
// 动作表刻意正交：一个动作只管一件事，AI 不需要在"该用哪个"上纠结。
//
//	snapshot 看状态 / click 点 / hover 悬停 / fill 填输入框
//	press 按键 / type 打字（这两个作用于当前焦点，页面内外通用）
//	eval 求值 / wait 等条件 / assert 下结论
func (s *Server) runStep(ctx context.Context, sid string, st batchStep) (string, stepExtra, error) {
	base := stepArgs{Intent: st.Intent, Expect: st.Expect}
	point := clickArgs{SessionID: sid, stepArgs: base, Ref: st.Ref, Match: st.Match, Selector: st.Selector, X: st.X, Y: st.Y}

	switch st.Action {
	case "snapshot":
		snap, shot, err := s.doSnapshot(ctx, sessionArgs{SessionID: sid, stepArgs: base})
		if err != nil {
			return "", stepExtra{}, err
		}
		return fmt.Sprintf("读到 %d 个可操作元素", len(snap.Elements)), stepExtra{
			Text: inspect.Summarize(snap), Elements: snap.Elements, Image: shot,
		}, nil
	case "click":
		msg, err := s.doClick(ctx, point)
		return msg, stepExtra{}, err
	case "hover":
		msg, err := s.doHover(ctx, point)
		return msg, stepExtra{}, err
	case "fill":
		msg, err := s.doFill(ctx, fillArgs{SessionID: sid, stepArgs: base, Ref: st.Ref, Match: st.Match, Selector: st.Selector, Text: st.Text})
		return msg, stepExtra{}, err
	case "press":
		msg, err := s.doPress(ctx, pressArgs{SessionID: sid, stepArgs: base, Key: st.Key})
		return msg, stepExtra{}, err
	case "type":
		msg, err := s.doTypeText(ctx, pressArgs{SessionID: sid, stepArgs: base, Key: st.Text})
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
		return "", stepExtra{}, fmt.Errorf("未知动作 %q（可用：snapshot/click/hover/fill/press/type/eval/wait/assert）", st.Action)
	}
}

// doRun 连续执行若干步；任何一步失败就停下，并把进度说清楚。
//
// 失败不返回 Go error：模型需要知道"跑到第几步、剩下没跑什么"，
// 用错误把它吞掉反而更糊涂。会话本身的状态在报告里，不靠这里的返回值。
func (s *Server) doRun(ctx context.Context, a runArgs) (string, runResult, error) {
	if len(a.Steps) == 0 {
		return "", runResult{}, errors.New("steps 不能为空")
	}
	if len(a.Steps) > maxRunSteps {
		return "", runResult{}, fmt.Errorf("一次最多 %d 步（收到 %d 步）", maxRunSteps, len(a.Steps))
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	// 这一批从哪到哪：切片按这段算，不按单步。
	batchStartMS := s.manager.NowOffset(a.SessionID)
	var beforeSeq int
	if events, err := s.manager.Events(ctx, a.SessionID); err == nil && len(events) > 0 {
		beforeSeq = events[len(events)-1].Seq
	}

	var b strings.Builder
	res := runResult{Steps: make([]runStepResult, 0, len(a.Steps))}
	for i, st := range a.Steps {
		msg, extra, err := s.runStep(ctx, a.SessionID, st)
		if err != nil {
			fmt.Fprintf(&b, "%d. %s ✗ %v\n", i+1, st.Action, err)
			fmt.Fprintf(&b, "已停止：后面 %d 步没有执行。", len(a.Steps)-i-1)
			res.Steps = append(res.Steps, runStepResult{
				Index: i + 1, Action: st.Action, OK: false, Text: err.Error(), Elements: extra.Elements, Value: extra.Value,
			})
			res.StoppedAt = i + 1
			s.markBatch(ctx, a.SessionID, beforeSeq, batchStartMS)
			s.attachCurrent(ctx, a.SessionID, a.Steps, &b, &res)
			return b.String(), res, nil
		}
		fmt.Fprintf(&b, "%d. %s ✓ %s\n", i+1, st.Action, msg)
		if extra.Text != "" {
			// 读页面结构这类动作，结果本身就是模型下一步的依据，原样贴出来
			b.WriteString(extra.Text)
			b.WriteString("\n")
		}
		res.Steps = append(res.Steps, runStepResult{
			Index: i + 1, Action: st.Action, OK: true, Text: msg, Elements: extra.Elements, Value: extra.Value, Image: extra.Image,
		})
	}
	if len(a.Steps) == 1 {
		fmt.Fprintf(&b, "完成。")
	} else {
		fmt.Fprintf(&b, "全部 %d 步执行完成。", len(a.Steps))
	}
	res.OK = true
	s.markBatch(ctx, a.SessionID, beforeSeq, batchStartMS)
	s.attachCurrent(ctx, a.SessionID, a.Steps, &b, &res)
	return b.String(), res, nil
}

// attachCurrent 跑完一批后，自动附上"现在界面长什么样"。
//
// 不做的话，模型跑完一批还得再调一次 snapshot 才知道结果——又是一次往返，
// 正好把批的意义抵消掉。这里不记步骤：它不是这次操作的一部分，是把结果给模型看。
func (s *Server) attachCurrent(ctx context.Context, sid string, steps []batchStep, b *strings.Builder, res *runResult) {
	// 最后一步已经是 snapshot 的话别重复取，直接把它那份提上来
	if len(steps) > 0 && steps[len(steps)-1].Action == "snapshot" {
		if n := len(res.Steps); n > 0 {
			res.Elements = res.Steps[n-1].Elements
			res.After = res.Steps[n-1].Image
			// 提上来就够了，别在步骤里再带一份
			res.Steps[n-1].Image = nil
		}
		return
	}
	insp, err := s.manager.Inspector(ctx, sid)
	if err != nil {
		return
	}
	snap, err := insp.Snapshot(ctx)
	if err != nil {
		return
	}
	res.Elements = snap.Elements
	if previewer, ok := insp.(interface {
		PreviewJPEG(ctx context.Context, quality int) ([]byte, error)
	}); ok {
		if raw, err := previewer.PreviewJPEG(ctx, 70); err == nil {
			res.After = raw
		}
	}
	if !res.OK {
		fmt.Fprintf(b, "\n停在这里的界面：%d 个可操作元素（见下图）", len(snap.Elements))
	}
}

// markBatch 把这一批的起止补进刚写入的事件里。
//
// 结束时间只有跑完才知道，所以先写事件、收尾时回填；失败只告警，
// 最差是这条片段退回"前后各 2 秒"，不影响报告本身。
func (s *Server) markBatch(ctx context.Context, sid string, beforeSeq int, startMS int64) {
	if startMS < 0 {
		return
	}
	endMS := s.manager.NowOffset(sid)
	if endMS < startMS {
		return
	}
	events, err := s.manager.Events(ctx, sid)
	if err != nil || len(events) == 0 {
		return
	}
	last := events[len(events)-1].Seq
	if err := s.manager.SetBatchWindow(ctx, sid, beforeSeq+1, last, startMS, endMS); err != nil {
		s.log.Warn("写入批次区间失败", "session", sid, "err", err)
	}
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
