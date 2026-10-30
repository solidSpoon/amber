package session

// step.go：「报告的一步」——把一次工具调用变成人能读的东西。
//
// 设计上刻意不引入第二个写入链路：报告就是从 Record() 这道闸门流出来的。
// 如果让模型事后另写一份报告，两边一定会对不上，而对不上的报告比没有更能骗人。
//
// 每步四样东西：
//   caption 做了什么（自动生成，不靠模型自觉）
//   intent  为什么做（调用方给，可空）
//   expect  期望看到什么（调用方给，可空）
//   shot    画面（自动截，失败也截——报告里最有用的正是失败那张）

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// Step 一次工具调用的报告元信息。
type Step struct {
	// Tool 工具名（click/fill/press/eval/snapshot/screenshot/assert）。
	Tool string
	// Args 原始参数，照旧进审计。
	Args any
	// Kind 步骤性质：action（默认）/ assert / note。
	Kind string
	// Intent 为什么做这一步，Expect 期望看到什么；都由调用方给，可空。
	Intent string
	Expect string
	// ShotRef 截步骤图时高亮哪个元素（引用号）；空表示只截图不标注。
	ShotRef string
	// Caption 调用方指定的人话描述；留空则由 captionFor 自动生成。
	Caption string
	// Shot 这一步的截图（相对会话目录），由 Record 自动填。
	Shot string
	// Passed 非空时覆盖成败判定（断言用：断言不成立 ≠ 工具调用失败）。
	Passed *bool
}

// describeElement 是 checkDescriptions 用到的能力（测试里的假检查器不实现）。
type describeElement interface {
	Describe(ref string) (inspect.Element, bool)
}

// stepShooter 能截「带高亮」的步骤图的检查器（测试里的假检查器不实现）。
type stepShooter interface {
	StepJPEG(ctx context.Context, path, ref string) (int64, error)
}

// describe 把参数里的元素引用变成「角色 + 名字」，用于自动生成 caption。
func describe(insp Inspector, step Step) *inspect.Element {
	describer, ok := insp.(describeElement)
	if !ok || step.ShotRef == "" {
		return nil
	}
	if el, ok := describer.Describe(step.ShotRef); ok {
		return &el
	}
	return nil
}

// captionFor 生成「点击「保存」按钮」这种句子。
//
// 原则：能确定就写具体（元素名 + 角色），不能确定就老实写参数，
// 绝不编。报告里出现一句编的话，整份报告的可信度就没了。
func captionFor(step Step, el *inspect.Element) string {
	args := flattenArgs(step.Args)
	name := ""
	role := ""
	if el != nil {
		name = strings.TrimSpace(el.Name)
		role = roleLabel(el.Role)
	}
	target := name
	if target != "" && role != "" {
		target = fmt.Sprintf("「%s」%s", name, role)
	} else if target != "" {
		target = fmt.Sprintf("「%s」", target)
	}

	ref := strings.TrimSpace(args.str("ref"))
	byRef := func(verb string) string {
		// 有引用号但元素没有可读名字：写引用号 + 角色，绝不退化成坐标
		// （ref 点击的坐标恒为 0,0，写成「点击 (0, 0)」纯属误导）
		if el != nil && el.Role != "" {
			return fmt.Sprintf("%s %s（%s）", verb, ref, el.Role)
		}
		return fmt.Sprintf("%s %s", verb, ref)
	}

	switch step.Tool {
	case "click":
		if target != "" {
			return "点击" + target
		}
		if ref != "" {
			return byRef("点击")
		}
		if x, y, ok := args.coord(); ok {
			return fmt.Sprintf("点击 (%s, %s)", x, y)
		}
		return "点击页面"
	case "fill":
		text := truncateString(args.str("text"), 60)
		if target != "" {
			return fmt.Sprintf("在%s填入 %s", target, quote(text))
		}
		if ref != "" {
			return fmt.Sprintf("在 %s 填入 %s", ref, quote(text))
		}
		return fmt.Sprintf("填入 %s", quote(text))
	case "press":
		return "按下 " + strings.TrimSpace(fmt.Sprint(args["key"]))
	case "eval":
		return "执行脚本 " + quote(truncateString(args.str("script"), 70))
	case "snapshot":
		return "读取页面结构"
	case "screenshot":
		return "截图留证"
	case "assert":
		return truncateString(args.str("claim"), 120)
	}
	return step.Tool
}

// roleLabel 把可访问性角色翻译成人话（拼在元素名后面）。
func roleLabel(role string) string {
	switch role {
	case "button":
		return "按钮"
	case "link":
		return "链接"
	case "textbox", "input", "textarea":
		return "输入框"
	case "combobox", "select":
		return "下拉框"
	case "checkbox":
		return "复选框"
	case "switch":
		return "开关"
	case "tab":
		return "标签页"
	case "menuitem":
		return "菜单项"
	case "heading":
		return ""
	case "video":
		return "播放器"
	}
	return ""
}

// argsView 是对已拍平的参数做轻量取值，避免到处写类型断言。
type argsView map[string]any

func flattenArgs(args any) argsView {
	switch v := args.(type) {
	case nil:
		return argsView{}
	case argsView:
		return v
	case map[string]any:
		return argsView(v)
	}
	return argsView{}
}

func (a argsView) str(key string) string {
	if v, ok := a[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (a argsView) coord() (string, string, bool) {
	x, hasX := a["x"]
	y, hasY := a["y"]
	if !hasX || !hasY {
		return "", "", false
	}
	return fmt.Sprint(x), fmt.Sprint(y), true
}

func quote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "（空）"
	}
	return "「" + s + "」"
}

func truncateString(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

// captureShot 截这一步的画面，返回相对会话目录的路径。
//
// 失败也要截：报告里读者最想看的就是「出错时屏幕长什么样」。
// 截不到不算错误（CDP 断了、应用崩了都可能导致），只是这一步没有图。
func (m *Manager) captureShot(ctx context.Context, sessDir string, insp Inspector, step Step) string {
	shooter, ok := insp.(stepShooter)
	if !ok {
		return ""
	}
	rel := filepath.Join("shots", fmt.Sprintf("%s-%s.jpg", time.Now().Format("150405.000"), sanitizeTool(step.Tool)))
	path := filepath.Join(sessDir, rel)
	if _, err := shooter.StepJPEG(ctx, path, step.ShotRef); err != nil {
		m.log.Warn("步骤截图失败", "err", err, "tool", step.Tool)
		return ""
	}
	return rel
}

func sanitizeTool(tool string) string {
	var b strings.Builder
	for _, r := range tool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// clipName 是某一步的短视频文件名（缓存在会话目录里）。
// 文件名带窗口版本号：切片规则变了，老的缓存不能拿来复用。
func clipName(seq int) string {
	return filepath.Join("clips", fmt.Sprintf("step2-%04d.mp4", seq))
}

// clipWindow 算出第 idx 步该切哪一段：这一步自己那 2 秒。
//
// 起点在动作前 0.6 秒、终点在动作后 1.4 秒：短到能一眼看完，又不至于把整段
// 录像切碎。窗口不随邻居挪动——相邻两步各看各的那 2 秒，谁也不会把别人的
// 画面当成自己的重复播一遍（之前「前后各 2 秒」就会重叠成那样）。
func clipWindow(target store.Event) (startMS, lengthMS int64) {
	start := target.VideoOffsetMS - 600
	if start < 0 {
		start = 0
	}
	return start, 2000
}

// 保证 store.Event 里的字段在本包里都用到（避免只加了一半）。
var _ = store.Event{}.Shot

// Assert 记录一条断言（报告里的 ✅/❌）。
//
// 断言和动作走同一条闸门，区别只有两点：kind=assert，以及成败由 claim 决定
// 而不是由「工具调用有没有报错」决定——断言不成立本来就是正常结果。
func (m *Manager) Assert(ctx context.Context, id, claim string, passed bool, evidence string) error {
	step := Step{
		Tool:   "assert",
		Kind:   "assert",
		Args:   map[string]any{"claim": claim, "ok": passed, "evidence": evidence},
		Passed: &passed,
		// 断言不再额外写 intent：claim 本身就是报告里那句话，重复一遍只是噪音
	}
	msg := "断言成立"
	if !passed {
		msg = "断言不成立"
	}
	if evidence != "" {
		msg += "：" + truncateString(evidence, 300)
	}
	_, _, err := m.Record(ctx, id, step, func(context.Context, Inspector) (string, string, error) {
		return msg, "", nil
	})
	return err
}

// Clip 返回「这一步自己的那几秒」的短视频路径（按需切片并缓存）。
//
// 窗口按步切：从上一动作开始，到这一动作之后 1.2 秒，且不越过下一步。
// 不用「前后各 2 秒」的固定窗口——相邻几步会因此大面积重叠，同一段画面
// 在十几个缩略图里各循环一遍，页面上就是「窗口反复变大变小」的错觉。
//
// 为什么是懒切片：报告里没人看的那一步不该在收尾时花时间编码。
func (m *Manager) Clip(ctx context.Context, id string, seq int) (string, error) {
	sess, err := m.Get(ctx, id)
	if err != nil {
		return "", err
	}
	events, err := m.store.ListEvents(ctx, id)
	if err != nil {
		return "", err
	}
	var target *store.Event
	for i := range events {
		if events[i].Seq == seq {
			target = &events[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("没有第 %d 步", seq)
	}
	if target.VideoOffsetMS < 0 || sess.VideoPath == "" {
		return "", fmt.Errorf("这一步没有对应录像")
	}

	out := filepath.Join(sess.Dir, clipName(seq))
	if info, err := os.Stat(out); err == nil && info.Size() > 0 {
		return out, nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}

	startMS, lengthMS := clipWindow(*target)
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", fmt.Sprintf("%.2f", float64(startMS)/1000),
		"-i", sess.VideoPath,
		"-t", fmt.Sprintf("%.2f", float64(lengthMS)/1000),
		// 报告里内联播放，960 宽足够看清界面，体积只有 1080p 的三分之一
		"-vf", "scale='min(960,iw)':-2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", "-an",
		out,
	}
	if raw, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("切片失败: %v (%s)", err, strings.TrimSpace(string(raw)))
	}
	return out, nil
}
