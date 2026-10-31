package session

// step.go：「记录的一步」——把一次工具调用变成人能读的东西。
//
// 设计上刻意不引入第二个写入链路：记录就是从 Record() 这道闸门流出来的。
// 如果让模型事后另写一份记录，两边一定会对不上，而对不上的记录比没有更能骗人。
//
// 每步四样东西：
//   caption 做了什么（自动生成，不靠模型自觉）
//   intent  为什么做（调用方给，可空）
//   expect  期望看到什么（调用方给，可空）
//   shot    画面（自动截，失败也截——记录里最有用的正是失败那张）

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// Step 一次工具调用的记录元信息。
type Step struct {
	// Tool 工具名（click/fill/press/eval/capture/screenshot/window）。
	Tool string
	// Args 原始参数，照旧进审计。
	Args any
	// Kind 步骤性质：action（默认）/ note。
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
	// AlwaysShot 这一步成功也要配图（capture 这种「这一步的价值就是那张图」的动作）。
	AlwaysShot bool
}

// describeElement 是 checkDescriptions 用到的能力（测试里的假检查器不实现）。
type describeElement interface {
	Describe(ref string) (inspect.Element, bool)
}

// elementHighlighter 能在页面上框出某个元素（整屏抓帧之前先画框）。
type elementHighlighter interface {
	Highlight(ctx context.Context, ref string) error
	ClearHighlight(ctx context.Context) error
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
// 绝不编。记录里出现一句编的话，整份记录的可信度就没了。
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
	case "hover":
		if target != "" {
			return "把鼠标移到" + target + "上"
		}
		if ref != "" {
			return byRef("移入")
		}
		if x, y, ok := args.coord(); ok {
			return fmt.Sprintf("移入坐标 (%s, %s)", x, y)
		}
		return "移入元素"
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
	case "snapshot", "capture":
		return "看一眼当前界面"
	case "wait":
		return "等待界面就绪"
	case "sleep":
		// ms 是数字不是字符串，用 fmt 兜住；写成"暂停  毫秒"读者会以为丢了一个字
		return "暂停 " + strings.TrimSpace(fmt.Sprint(args["ms"])) + " 毫秒"
	case "screenshot":
		return "截图留证"
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

// HasVisual 这一步值不值得配一段录像片段。
//
// capture 要：它就是"看一眼当前界面"，那一眼本身就是证据。
// eval / wait / sleep 只取值、等条件、等时间，片段和上一步一样，白占地方。
//
// 注意它只管"要不要留录像片段"，不管失败截图：失败的那一步永远要截一张。
func HasVisual(tool string) bool {
	switch tool {
	case "eval", "wait", "sleep":
		return false
	default:
		return true
	}
}

// captureShot 截这一步的画面，返回相对会话目录的路径。
//
// 失败也要截：记录里读者最想看的就是「出错时屏幕长什么样」。
// 截不到不算错误（CDP 断了、应用崩了都可能导致），只是这一步没有图。
func (m *Manager) captureShot(ctx context.Context, id, sessDir string, insp Inspector, step Step) string {
	// 要先高亮再抓帧：高亮是页面里的覆盖层，整屏抓帧能把它一起拍进去。
	if step.ShotRef != "" {
		if highlighter, ok := insp.(elementHighlighter); ok {
			if err := highlighter.Highlight(ctx, step.ShotRef); err == nil {
				defer func() { _ = highlighter.ClearHighlight(context.WithoutCancel(ctx)) }()
				// 等一帧再抓：刚插入的覆盖层可能还没合成，拍出来会缺框
				time.Sleep(80 * time.Millisecond)
			}
		}
	}
	rel, err := m.SaveScreenJPEG(ctx, id, step.Tool, shotMaxWidth)
	if err != nil {
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
// 文件名按"批次起点"命名：同一批的每一步共用同一条片段，不重复编码。
//
// 用批次的原始起点、不是切片起点：切片起点会被 -2 秒截到 0，
// 早前几步的片段就会撞成同一个文件，互相覆盖。
func clipName(startMS int64) string {
	return filepath.Join("clips", fmt.Sprintf("batch-%08d.mp4", startMS))
}

// clipWindow 算一条片段该切哪一段。
//
// 单位是"一段连续操作"（一次 run 调用），不是单步：一批里的相邻两步可能只隔
// 0.2 秒，每步各切前后 2 秒会把同一段画面重复十几遍。一批 = 一条录像，
// 从这批第一步前 2 秒，到最后一步后 2 秒。
//
// 没有批次信息时（老数据、单步写入）退回前后各 2 秒。
func clipWindow(target store.Event) (startMS, lengthMS int64) {
	if target.BatchStartMS >= 0 && target.BatchEndMS >= 0 {
		start := target.BatchStartMS - 2000
		if start < 0 {
			start = 0
		}
		end := target.BatchEndMS + 2000
		if end <= start {
			end = start + 2000
		}
		return start, end - start
	}
	start := target.VideoOffsetMS - 2000
	if start < 0 {
		start = 0
	}
	return start, 4000
}

// masterVideo 整场录像（母片）的路径。收尾时它会被删掉，只剩片段。
//
// 位置由容器启动脚本约定（RIG_OUT=/out），不再存进库里：
// 同一个事实存两份，早晚会有新会话写出一个对不上的路径。
func masterVideo(sess store.Session) string {
	return filepath.Join(sess.Dir, "video", "session.mp4")
}

// clipPath 这一步的片段落在哪：同一批共用一个文件，按批起点命名。
func clipPath(sess store.Session, target store.Event) string {
	key := target.VideoOffsetMS
	if target.BatchStartMS >= 0 {
		key = target.BatchStartMS
	}
	return filepath.Join(sess.Dir, clipName(key))
}

// Clip 返回某一步所在那一段的短视频路径（没有缓存就现切）。
//
// 收尾之后母片已经删了（见 cutClips），所以正常路径是「直接命中缓存」；
// 这里的现切只服务会话还在跑、或老会话母片还在的情况。
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
	if target.VideoOffsetMS < 0 {
		return "", fmt.Errorf("这一步没有对应画面")
	}

	out := clipPath(sess, *target)
	if info, err := os.Stat(out); err == nil && info.Size() > 0 {
		return out, nil
	}
	startMS, lengthMS := clipWindow(*target)
	if err := encodeClip(ctx, masterVideo(sess), out, startMS, lengthMS); err != nil {
		return "", err
	}
	return out, nil
}

// ClipReady 这一步的画面能不能给：已经切好的片段在，或者母片还在（可以现切）。
//
// 收尾会删掉母片，所以判断不能只看母片在不在——老会话的片段依然要能播。
func (m *Manager) ClipReady(sess store.Session, target store.Event) bool {
	if target.VideoOffsetMS < 0 || !HasVisual(target.Tool) {
		return false
	}
	if info, err := os.Stat(clipPath(sess, target)); err == nil && info.Size() > 0 {
		return true
	}
	info, err := os.Stat(masterVideo(sess))
	return err == nil && info.Size() > 0
}

// cutClips 把每一批的片段一次性切好，切完母片就可以删了。
//
// 为什么要提前切：整场录像不再保留，删之前必须把记录里会被引用的画面落下来；
// 一批只有几秒，切一遍比重放整场录像便宜得多。
// 返回切了多少条（含命中缓存的）。
func (m *Manager) cutClips(ctx context.Context, sess store.Session) (int, error) {
	events, err := m.store.ListEvents(ctx, sess.ID)
	if err != nil {
		return 0, err
	}
	master := masterVideo(sess)
	if info, err := os.Stat(master); err != nil || info.Size() == 0 {
		return 0, nil
	}
	cut := 0
	var failed error
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		if !m.ClipReady(sess, e) {
			continue
		}
		out := clipPath(sess, e)
		if seen[out] {
			continue
		}
		seen[out] = true
		startMS, lengthMS := clipWindow(e)
		if err := encodeClip(ctx, master, out, startMS, lengthMS); err != nil {
			// 一条切失败不该连累其他步骤的画面，记下第一条原因继续
			if failed == nil {
				failed = err
			}
			continue
		}
		cut++
	}
	return cut, failed
}

// encodeClip 从母片里切一段出来。
//
// 切片用固定窗口而不是「等动画结束」——可预期，也够用。
func encodeClip(ctx context.Context, master, out string, startMS, lengthMS int64) error {
	if info, err := os.Stat(out); err == nil && info.Size() > 0 {
		return nil
	}
	if info, err := os.Stat(master); err != nil || info.Size() == 0 {
		return errors.New("母片已归档，切片不可用")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", fmt.Sprintf("%.2f", float64(startMS)/1000),
		"-i", master,
		"-t", fmt.Sprintf("%.2f", float64(lengthMS)/1000),
		// 记录里内联播放，960 宽足够看清界面，体积只有 1080p 的三分之一
		"-vf", "scale='min(960,iw)':-2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", "-an",
		out,
	}
	if raw, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("切片失败: %v (%s)", err, strings.TrimSpace(string(raw)))
	}
	return nil
}
