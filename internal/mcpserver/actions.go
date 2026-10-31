package mcpserver

// actions.go：动作的实现体 + 「批」。
//
// 每个动作只有一份实现：顶层工具和 batch 都走这里。
// 这样批里的第 3 步和第 3 次单独调用是同一段代码、同一条记录路径，
// 记录里看不出区别，也不会出现"批里能做的事单独调做不了"这种分叉。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

// doCapture 看一眼当前界面，并把这一眼记成记录里的一步。
//
// 工具 capture 和 run 的最后一步都走这里：同一个动作、同一份记录路径。
// 画面是整块屏幕（session 层对着虚拟屏抓帧），所以原生对话框、系统菜单也在里面。
func (s *Server) doCapture(ctx context.Context, a captureArgs) (inspect.Snapshot, string, []byte, error) {
	var snap inspect.Snapshot
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool:       "capture",
		Args:       map[string]any{"for": a.For, "timeoutMs": a.TimeoutMS},
		Intent:     a.Intent,
		Expect:     a.Expect,
		AlwaysShot: true,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		// 等不到条件不算这一步失败：capture 的价值是"留下当时的样子"，
		// 没等到就照实说明没等到，画面照样带回去。
		waited, waitErr := waitForCondition(ctx, insp, a.For, a.TimeoutMS, defaultCaptureWait)
		if waitErr != nil && ctx.Err() != nil {
			return "", "", waitErr
		}
		got, err := insp.Snapshot(ctx)
		if err != nil {
			return "", "", err
		}
		snap = got
		text := inspect.Summarize(got)
		switch {
		case waitErr != nil:
			text = fmt.Sprintf("没等到「%s」（等了 %.1f 秒），下面是当时的界面。\n", a.For, waited.Seconds()) + text
		case waited > 0:
			text = fmt.Sprintf("等 %.1f 秒后界面稳定。\n", waited.Seconds()) + text
		}
		return text, "", nil
	})
	if err != nil {
		return inspect.Snapshot{}, "", nil, err
	}
	// 记录里那张图就是回给模型的这张：一次抓帧，两处用同一份证据。
	var shot []byte
	if rec.Step.Shot != "" {
		if sess, gerr := s.manager.Get(ctx, a.SessionID); gerr == nil && sess.Dir != "" {
			if raw, rerr := os.ReadFile(filepath.Join(sess.Dir, rec.Step.Shot)); rerr == nil {
				shot = raw
			}
		}
	}
	return snap, rec.Result, shot, nil
}

// waitForCondition 轮询一个 JS 条件，返回等了多久（没给条件就是 0 秒、不等待）。
func waitForCondition(ctx context.Context, insp session.Inspector, expr string, timeoutMS, def int) (time.Duration, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, nil
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(def) * time.Millisecond
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	started := time.Now()
	var lastErr error
	for {
		raw, evalErr := insp.Evaluate(ctx, "Boolean("+expr+")")
		if evalErr != nil {
			// 元素还没出现时求值会抛错，这本身就是"还没到"，继续等
			lastErr = evalErr
		} else if strings.TrimSpace(string(raw)) == "true" {
			return time.Since(started), nil
		}
		if time.Since(started) >= timeout {
			msg := fmt.Sprintf("等待超时（%s）：%s", timeout, expr)
			if lastErr != nil {
				msg += "；最后一次求值错误：" + lastErr.Error()
			}
			return time.Since(started), errors.New(msg)
		}
		select {
		case <-ctx.Done():
			return time.Since(started), ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
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
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "click", Args: map[string]any{"ref": a.Ref, "x": a.X, "y": a.Y},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if a.Ref != "" {
			if err := insp.Click(ctx, a.Ref); err != nil {
				return "", "", err
			}
			// 记录里写元素的名字，不写引用号：读者不认识 q1/e5
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
	return rec.Result, err
}

// doHover 把鼠标移到元素上：悬停才出现的浮层（词典释义）用它。
func (s *Server) doHover(ctx context.Context, a clickArgs) (string, error) {
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
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
	return rec.Result, err
}

// doFill 覆盖填写文本，可选回车提交。
func (s *Server) doFill(ctx context.Context, a fillArgs) (string, error) {
	ref, err := s.resolveRef(ctx, a.SessionID, a.Ref, a.Match, a.Selector)
	if err != nil {
		return "", err
	}
	a.Ref = ref
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "fill", Args: map[string]any{"ref": a.Ref, "text": a.Text},
		Intent: a.Intent, Expect: a.Expect, ShotRef: a.Ref,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		if err := insp.Fill(ctx, a.Ref, a.Text); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("已填入 %q", a.Text), "", nil
	})
	return rec.Result, err
}

// doPress 给当前焦点发一次按键（组合键也行，如 ctrl+l）。
//
// 走系统层而不是页面的键盘事件：真实按键页面和原生对话框都收得到，
// 一套动作覆盖两种情况，AI 不用判断"该用哪个按键动作"。
func (s *Server) doPress(ctx context.Context, a pressArgs) (string, error) {
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "press", Args: map[string]any{"key": a.Key}, Intent: a.Intent, Expect: a.Expect,
		Caption: "按下 " + a.Key,
	}, func(ctx context.Context, _ session.Inspector) (string, string, error) {
		if err := s.manager.DesktopKey(ctx, a.SessionID, a.Key); err != nil {
			return "", "", err
		}
		return "已按下 " + a.Key, "", nil
	})
	return rec.Result, err
}

// doTypeText 往当前焦点敲一段字（原生对话框里输路径用它）。
func (s *Server) doTypeText(ctx context.Context, a typeArgs) (string, error) {
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "type", Args: map[string]any{"text": a.Text}, Intent: a.Intent, Expect: a.Expect,
		Caption: "输入「" + a.Text + "」",
	}, func(ctx context.Context, _ session.Inspector) (string, string, error) {
		if err := s.manager.DesktopType(ctx, a.SessionID, a.Text); err != nil {
			return "", "", err
		}
		return "已输入 " + a.Text, "", nil
	})
	return rec.Result, err
}

// doEval 在页面里求值。
func (s *Server) doEval(ctx context.Context, a evalArgs) (string, any, error) {
	var out any
	_, err := s.manager.Record(ctx, a.SessionID, session.Step{
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
//
// 和 sleep 严格分工：wait 只等条件（等不到 = 这一步失败），sleep 只等时间（不会失败）。
// 两者不接受对方的参数，写混了直接报错——省得调用方拿 for:"true" 假装睡眠。
func (s *Server) doWait(ctx context.Context, a waitArgs) (string, error) {
	expr := strings.TrimSpace(a.For)
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}

	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "wait", Args: map[string]any{"for": expr, "timeoutMs": timeout.Milliseconds()},
		Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, insp session.Inspector) (string, string, error) {
		elapsed, werr := waitForCondition(ctx, insp, expr, a.TimeoutMS, 5000)
		if werr != nil {
			return "", "", werr
		}
		return fmt.Sprintf("条件成立（等了 %.1f 秒）", elapsed.Seconds()), "", nil
	})
	return rec.Result, err
}

// doSleep 等一段固定时间。
//
// 它不是"没写好的 wait"：动画、限流、防抖这些场景就是需要一段确定的时长，
// 而且它不该因为"条件没成立"而失败。要等条件成立用 wait。
func (s *Server) doSleep(ctx context.Context, a sleepArgs) (string, error) {
	waited := time.Duration(a.MS) * time.Millisecond
	rec, err := s.manager.Record(ctx, a.SessionID, session.Step{
		Tool: "sleep", Args: map[string]any{"ms": a.MS}, Intent: a.Intent, Expect: a.Expect,
	}, func(ctx context.Context, _ session.Inspector) (string, string, error) {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(waited):
		}
		return fmt.Sprintf("等了 %d 毫秒", a.MS), "", nil
	})
	return rec.Result, err
}

// ---- 批 -------------------------------------------------------------------

const (
	maxActSteps = 50
	// maxSleepMS 一次 sleep 的上限：再长就该交给 wait 等条件，而不是干等
	maxSleepMS = 60000
	// defaultCaptureWait capture 没写 timeoutMs 时的等待上限
	defaultCaptureWait = 3000
)

// batchStep 批里的一步：动作名 + 该动作自己的参数。
//
// 故意做成扁平结构而不是嵌套 JSON：模型写错一层就整个批跑不了，
// 扁平至少能一眼看出哪一项写错了。每个动作需要哪些字段写在 action 的说明里，
// 执行前还有一道自检（见 validateStep），缺什么当场说缺什么。
type batchStep struct {
	Action string `json:"action" jsonschema:"动作，见工具说明的对照表：click/hover/fill/press/type/eval/wait/sleep/capture"`
	stepArgs

	Ref       string  `json:"ref,omitempty" jsonschema:"click/hover/fill：元素引用号（capture 给的，如 e3）"`
	Match     string  `json:"match,omitempty" jsonschema:"click/hover/fill：按可见文字找元素（页面刚变、还没有引用号时用）"`
	Selector  string  `json:"selector,omitempty" jsonschema:"click/hover/fill：按 CSS 选择器找元素"`
	X         float64 `json:"x,omitempty" jsonschema:"click/hover：页面坐标 X（和 y 一起用，兜底寻找方式）"`
	Y         float64 `json:"y,omitempty" jsonschema:"click/hover：页面坐标 Y"`
	Text      string  `json:"text,omitempty" jsonschema:"fill/type：要输入的文字"`
	Key       string  `json:"key,omitempty" jsonschema:"press：按键名，组合键写成 ctrl+l"`
	Script    string  `json:"script,omitempty" jsonschema:"eval：在页面里求值的 JS 表达式"`
	For       string  `json:"for,omitempty" jsonschema:"wait/capture：等它成立的 JS 条件；sleep 不接受这个字段"`
	MS        int     `json:"ms,omitempty" jsonschema:"sleep：要等多少毫秒（1~60000）；wait 不接受这个字段"`
	TimeoutMS int     `json:"timeoutMs,omitempty" jsonschema:"wait/capture：最多等多久（毫秒），默认 wait 5000、capture 3000"`
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

// ---- 动作表 ---------------------------------------------------------------

// actionSpec 一个动作的全部定义。会漂的三处都由它长出来：
// JSON Schema 的 enum、run 工具说明里的对照表、执行前的字段自检。
//
// 分三处各写一份的代价是真实的：描述里写着一个动作、代码里没有分支，
// 调用方拿到一句"未知动作"，却看不出是文档错了还是参数错了。
type actionSpec struct {
	// Name 对外宣传的动作名（已归一，别名不在这里）
	Name string
	// Need 必填字段，用人话写：既是工具说明的一列，也是自检报错的开头
	Need string
	// Note 这个动作是干什么的（工具说明里跟在字段后面的一句话）
	Note string
	// Check 执行前核对字段和动作对不对得上；nil 表示没有额外要求
	Check func(batchStep) error
	// Run 真正执行一步：返回给模型看的一句话 + 额外载荷
	Run func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error)
}

// needPoint 是"点哪个元素"的四种写法，click 和 hover 共用。
const needPoint = "ref 或 match 或 selector 或 x+y"

// hasPoint 判断这一组参数有没有指定目标元素。
func hasPoint(st batchStep) bool {
	return st.Ref != "" || st.Match != "" || st.Selector != "" || (st.X != 0 && st.Y != 0)
}

// actionTable 是动作的唯一清单：枚举、说明、自检、执行体都在这里。
var actionTable = []actionSpec{
	{
		Name: "click", Need: needPoint,
		Note: "点元素",
		Check: func(st batchStep) error {
			if hasPoint(st) {
				return nil
			}
			return fmt.Errorf("click 需要 %s 之一", needPoint)
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doClick(ctx, clickArgs{
				SessionID: sid, stepArgs: base,
				Ref: st.Ref, Match: st.Match, Selector: st.Selector, X: st.X, Y: st.Y,
			})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "hover", Need: needPoint,
		Note: "把鼠标停在元素上（悬停才出现的浮层，如词典）",
		Check: func(st batchStep) error {
			if hasPoint(st) {
				return nil
			}
			return fmt.Errorf("hover 需要 %s 之一", needPoint)
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doHover(ctx, clickArgs{
				SessionID: sid, stepArgs: base,
				Ref: st.Ref, Match: st.Match, Selector: st.Selector, X: st.X, Y: st.Y,
			})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "fill", Need: "ref/match/selector + text",
		Note: "覆盖填写输入框",
		Check: func(st batchStep) error {
			if st.Ref == "" && st.Match == "" && st.Selector == "" {
				return errors.New("fill 需要 ref、match/selector 之一")
			}
			if st.Text == "" {
				return errors.New("fill 需要 text（要填入的内容）")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doFill(ctx, fillArgs{
				SessionID: sid, stepArgs: base,
				Ref: st.Ref, Match: st.Match, Selector: st.Selector, Text: st.Text,
			})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "press", Need: "key",
		Note: "按键，组合键写成 ctrl+l；页面和原生对话框都收得到",
		Check: func(st batchStep) error {
			if strings.TrimSpace(st.Key) == "" {
				return errors.New("press 需要 key（如 Enter、ArrowRight、ctrl+l）")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doPress(ctx, pressArgs{SessionID: sid, stepArgs: base, Key: st.Key})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "type", Need: "text",
		Note: "往当前焦点打字",
		Check: func(st batchStep) error {
			if st.Text == "" {
				return errors.New("type 需要 text（要输入的文字）")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doTypeText(ctx, typeArgs{SessionID: sid, stepArgs: base, Text: st.Text})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "eval", Need: "script",
		Note: "在页面里求值，返回值原样给你",
		Check: func(st batchStep) error {
			if strings.TrimSpace(st.Script) == "" {
				return errors.New("eval 需要 script（在页面里求值的 JS 表达式）")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			pretty, val, err := s.doEval(ctx, evalArgs{SessionID: sid, stepArgs: base, Script: st.Script})
			if err != nil {
				return "", stepExtra{}, err
			}
			return pretty, stepExtra{Value: val}, nil
		},
	},
	{
		// wait 和 sleep 各自拒绝对方的字段：写混了当场说清楚，
		// 省得调用方拿 for:"true" 假装睡眠。
		Name: "wait", Need: "for（等条件成立；只想等时间用 sleep）",
		Note: "等条件成立，等不到算这一步失败",
		Check: func(st batchStep) error {
			if strings.TrimSpace(st.For) == "" {
				return errors.New("wait 需要 for（等它成立的 JS 条件）；只想等一段时间请改用 sleep")
			}
			if st.MS != 0 {
				return errors.New("wait 不接受 ms：要等固定时长请用 sleep")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doWait(ctx, waitArgs{SessionID: sid, stepArgs: base, For: st.For, TimeoutMS: st.TimeoutMS})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "sleep", Need: "ms（等固定时长；等条件成立用 wait）",
		Note: "等固定时长，不会失败",
		Check: func(st batchStep) error {
			if st.MS <= 0 {
				return errors.New("sleep 需要 ms（要等多少毫秒）；等条件成立请用 wait")
			}
			if st.For != "" {
				return errors.New("sleep 不接受 for：要等条件成立请用 wait")
			}
			if st.MS > maxSleepMS {
				return fmt.Errorf("sleep 最多 %d 毫秒（收到 %d）", maxSleepMS, st.MS)
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			msg, err := s.doSleep(ctx, sleepArgs{SessionID: sid, stepArgs: base, MS: st.MS})
			return msg, stepExtra{}, err
		},
	},
	{
		Name: "capture", Need: "可选 for + timeoutMs",
		Note: "看一眼并留证，必须是最后一步",
		Check: func(st batchStep) error {
			if st.MS != 0 {
				return errors.New("capture 不接受 ms：它等的是 for 条件；单纯等待请用 sleep")
			}
			return nil
		},
		Run: func(ctx context.Context, s *Server, sid string, base stepArgs, st batchStep) (string, stepExtra, error) {
			snap, text, shot, err := s.doCapture(ctx, captureArgs{
				SessionID: sid, stepArgs: base, For: st.For, TimeoutMS: st.TimeoutMS,
			})
			if err != nil {
				return "", stepExtra{}, err
			}
			return fmt.Sprintf("界面：%s（%d 个可操作元素）", snap.Title, len(snap.Elements)),
				stepExtra{Text: text, Elements: snap.Elements, Image: shot}, nil
		},
	},
}

// lookupAction 按名字取动作定义。
func lookupAction(name string) (actionSpec, bool) {
	for _, spec := range actionTable {
		if spec.Name == name {
			return spec, true
		}
	}
	return actionSpec{}, false
}

// actionNames 给 JSON Schema 的 enum 用。
func actionNames() []any {
	names := make([]any, 0, len(actionTable))
	for _, a := range actionTable {
		names = append(names, a.Name)
	}
	return names
}

// actionHelp 拼一句「可用动作」，错误信息和 schema 说明都用它。
func actionHelp() string {
	parts := make([]string, 0, len(actionTable))
	for _, a := range actionTable {
		parts = append(parts, a.Name+"（"+a.Need+"）")
	}
	return strings.Join(parts, " / ")
}

// actionLines 是工具说明里的动作对照表，一行一个动作（与 schema 同一个来源）。
func actionLines() string {
	var b strings.Builder
	for _, a := range actionTable {
		fmt.Fprintf(&b, "  %-8s%s —— %s\n", a.Name, a.Need, a.Note)
	}
	return b.String()
}

// normalizeAction 把历史名字收进来：同一个动作只对外宣传一个名字。
//
// snapshot / wait_and_capture 是 capture 的旧写法（两者本来就返回同一个东西），
// 留着只为让旧脚本再来一次不至于报错，不再出现在工具清单和说明里。
func normalizeAction(name string) string {
	switch name {
	case "snapshot", "wait_and_capture":
		return "capture"
	}
	return name
}

// validateStep 在执行前把「字段和动作对不上」的情况说清楚。
//
// 为什么不在 schema 里做条件校验：JSON Schema 表达"click 要 ref 或坐标"要么写
// 一大堆 oneOf，要么做不到；而错误信息本来就需要一句人话，索性在这里一次说完。
func validateStep(st batchStep) error {
	spec, ok := lookupAction(st.Action)
	if !ok {
		return fmt.Errorf("未知动作 %q；可用动作：%s", st.Action, actionHelp())
	}
	if spec.Check == nil {
		return nil
	}
	return spec.Check(st)
}

// actStep 执行一步；返回一句给人看的结果 + 给模型的额外载荷。
//
// 动作表刻意正交：一个动作只管一件事，AI 不需要在"该用哪个"上纠结。
//
//	capture 看状态 / click 点 / hover 悬停 / fill 填输入框
//	press 按键 / type 打字（这两个作用于当前焦点，页面内外通用）
//	eval 求值 / wait 等条件 / sleep 等时间
func (s *Server) actStep(ctx context.Context, sid string, st batchStep) (string, stepExtra, error) {
	spec, ok := lookupAction(st.Action)
	if !ok {
		return "", stepExtra{}, fmt.Errorf("未知动作 %q；可用动作：%s", st.Action, actionHelp())
	}
	return spec.Run(ctx, s, sid, stepArgs{Intent: st.Intent, Expect: st.Expect}, st)
}

// doAct 连续执行若干步；任何一步失败就停下，并把进度说清楚。
//
// 失败不返回 Go error：模型需要知道"跑到第几步、剩下没跑什么"，
// 用错误把它吞掉反而更糊涂。会话本身的状态在记录里，不靠这里的返回值。
//
// 同理，服务端超时也不返回 error：那只会让客户端拿到一句没有上下文的
// "MCP tools/call returned an error"。超时照样按正常结果交回已完成的步骤、
// 停在第几步、以及当时屏幕的样子。
func (s *Server) doAct(ctx context.Context, a actArgs) (string, actResult, error) {
	if strings.TrimSpace(a.Intent) == "" {
		return "", actResult{}, errors.New("run 需要 intent：一句话说清这一批要干什么（记录里当这一段的小标题）")
	}
	if len(a.Steps) == 0 {
		return "", actResult{}, errors.New("steps 不能为空")
	}
	if len(a.Steps) > maxActSteps {
		return "", actResult{}, fmt.Errorf("一次最多 %d 步（收到 %d 步）", maxActSteps, len(a.Steps))
	}
	for i := range a.Steps {
		a.Steps[i].Action = normalizeAction(a.Steps[i].Action)
	}
	// 先把每一步的字段和动作对一遍：写错的那一步不该等到跑了八步才被发现。
	for i, st := range a.Steps {
		if err := validateStep(st); err != nil {
			return "", actResult{}, fmt.Errorf("第 %d 步（%s）：%w", i+1, st.Action, err)
		}
	}
	// 最后一步必须是收尾动作：等到界面稳定再把画面交回来。
	// 不这样要求的话，模型会拿"动作刚发出去"的中间状态当结果，结论就不可信了。
	last := a.Steps[len(a.Steps)-1]
	if last.Action != "capture" {
		return "", actResult{}, errors.New(
			"steps 的最后一步必须是 capture（参数 for、timeoutMs）：" +
				"它是这一批的收尾——等到界面稳定，再把画面和可操作元素带回来")
	}
	body := a.Steps[:len(a.Steps)-1]

	batchTimeout := s.batchTimeout()
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	// 这一批从哪到哪：切片按这段算，不按单步。
	batch := batchSpan{startMS: s.manager.NowOffset(a.SessionID)}
	if events, err := s.manager.Events(ctx, a.SessionID); err == nil && len(events) > 0 {
		batch.beforeSeq = events[len(events)-1].Seq
	}

	var b strings.Builder
	res := actResult{Steps: make([]actStepResult, 0, len(body))}
	started := time.Now()
	for i, st := range body {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(&b, "停止：整批超过 %s，第 %d 步还没开始。\n", batchTimeout, i+1)
			res.StoppedAt = i + 1
			res.OK = false
			s.closeBatch(ctx, a.SessionID, last, a.Intent, batch, &b, &res)
			return b.String(), res, nil
		}

		// 单步看门狗：eval 这类动作可能永远不返回，不能让它拖死整批。
		msg, extra, err := s.runStep(ctx, a.SessionID, st)

		if err != nil {
			fmt.Fprintf(&b, "%d. %s ✗ %v\n", i+1, st.Action, err)
			fmt.Fprintf(&b, "已停止：后面 %d 步没有执行。", len(body)-i-1)
			res.Steps = append(res.Steps, actStepResult{
				Index: i + 1, Action: st.Action, OK: false, Text: err.Error(), Elements: extra.Elements, Value: extra.Value,
			})
			res.StoppedAt = i + 1
			res.OK = false
			s.closeBatch(ctx, a.SessionID, last, a.Intent, batch, &b, &res)
			return b.String(), res, nil
		}
		fmt.Fprintf(&b, "%d. %s ✓ %s\n", i+1, st.Action, msg)
		if extra.Text != "" {
			// 读界面结构这类动作，结果本身就是模型下一步的依据，原样贴出来
			b.WriteString(extra.Text)
			b.WriteString("\n")
		}
		res.Steps = append(res.Steps, actStepResult{
			Index: i + 1, Action: st.Action, OK: true, Text: msg, Elements: extra.Elements, Value: extra.Value, Image: extra.Image,
		})
	}
	fmt.Fprintf(&b, "全部 %d 步执行完成（%.1f 秒）。", len(body), time.Since(started).Seconds())
	res.OK = true
	s.closeBatch(ctx, a.SessionID, last, a.Intent, batch, &b, &res)
	return b.String(), res, nil
}

// runStep 跑一步，带单步看门狗。
//
// 超时要换成一句能照着改的话：原始的 context deadline exceeded 只会让调用方
// 以为整个会话坏了，而实际只是这一步的脚本挂住了。
func (s *Server) runStep(ctx context.Context, sid string, st batchStep) (string, stepExtra, error) {
	deadline := s.stepDeadline(st)
	stepCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	msg, extra, err := s.actStep(stepCtx, sid, st)
	if err != nil && errors.Is(stepCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return msg, extra, fmt.Errorf("这一步超过 %s 还没返回（脚本挂住了？）；"+
			"要固定等一段时间用 sleep，要等条件用 wait", deadline)
	}
	return msg, extra, err
}

// batchSpan 是这一批在记录里的范围：回填批次区间要用它。
type batchSpan struct {
	beforeSeq int
	startMS   int64
}

// closeBatch 一批的收尾：补上收尾那一眼，再把这一段的时间区间写回事件。
//
// 正常跑完、某一步失败、整批超时——三种情况收尾完全一样，
// 所以只有这一条路径（各写一遍必然会出现"某条路上忘了回填批次"）。
func (s *Server) closeBatch(ctx context.Context, sid string, last batchStep, intent string, batch batchSpan, b *strings.Builder, res *actResult) {
	s.captureAfter(ctx, sid, last, b, res)
	s.markBatch(ctx, sid, batch.beforeSeq, batch.startMS, intent)
}

// batchTimeout 一批的总时限（配置项，必须小于 HTTP 中间件超时）。
func (s *Server) batchTimeout() time.Duration {
	if s.cfg != nil && s.cfg.BatchTimeout > 0 {
		return s.cfg.BatchTimeout
	}
	return 2 * time.Minute
}

// stepDeadline 单个动作的最长等待；给了 timeoutMs 的动作（等条件/收尾抓帧）
// 按它自己的时限放宽，别被看门狗提前砍掉。
func (s *Server) stepDeadline(st batchStep) time.Duration {
	base := 20 * time.Second
	if s.cfg != nil && s.cfg.StepTimeout > 0 {
		base = s.cfg.StepTimeout
	}
	if st.TimeoutMS > 0 {
		if own := time.Duration(st.TimeoutMS)*time.Millisecond + 5*time.Second; own > base {
			base = own
		}
	}
	return base
}

// captureAfter 执行收尾动作 capture：等到条件成立，再把界面带回来。
//
// 它和单独调 capture 是同一条实现：收尾那一眼同样进记录（同一件事不该有两种行为）。
// 等不到也照样把画面带回来——记录里能看到"这时候还没稳定"，比什么都不给强。
func (s *Server) captureAfter(ctx context.Context, sid string, step batchStep, b *strings.Builder, res *actResult) {
	started := time.Now()
	// 收尾不能用已经超时的 ctx：那时候正是最需要"最后一眼"的时候。
	capCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	snap, _, shot, err := s.doCapture(capCtx, captureArgs{
		SessionID: sid,
		stepArgs:  stepArgs{Intent: step.Intent, Expect: step.Expect},
		For:       step.For,
		TimeoutMS: step.TimeoutMS,
	})
	if err != nil {
		fmt.Fprintf(b, "\n收尾的 capture 没成功：%v", err)
		return
	}
	res.Elements = snap.Elements
	res.Shot = shot
	waited := time.Since(started)
	switch {
	case strings.TrimSpace(step.For) == "":
		fmt.Fprintf(b, "\n画面见下（capture 已记进记录）。")
	default:
		fmt.Fprintf(b, "\n界面已稳定，画面见下（capture 已记进记录，本次抓帧耗时 %.1f 秒）。", waited.Seconds())
	}
}

// markBatch 把这一批的起止和意图补进刚写入的事件里。
//
// 结束时间只有跑完才知道，所以先写事件、收尾时回填；失败只告警，
// 最差是这条片段退回"前后各 2 秒"，不影响记录本身。
func (s *Server) markBatch(ctx context.Context, sid string, beforeSeq int, startMS int64, intent string) {
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
	if err := s.manager.SetBatchMeta(ctx, sid, beforeSeq+1, last, startMS, endMS, intent); err != nil {
		s.log.Warn("写入批次区间失败", "session", sid, "err", err)
	}
}

func registerFeedbackTool(server *mcp.Server, s *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "amber_feedback",
		Description: "用完这套工作流后，把「哪里别扭、缺什么、希望怎么改」写下来。" +
			"这不是结论，也不会进记录——它只用于产品改进：被人看过之后统一变成改进项。" +
			"用起来不顺、某个动作每次都要绕、记录哪里看不懂，都可以写。rating 可选（1~5）。",
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
// 它不是验收证据，也不进记录：这是给产品改进用的输入。
// 单独列出来，是为了让模型知道"觉得哪里别扭"是有地方说的，
// 而不是在记录里写一句"这个工具不好用"就算完。
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
