package session

// record.go：「记录的一步」怎么落下来——偏移量、审计、批次回填。
//
// 这里是唯一的写入闸门：任何对外动作都必须经过 Record，否则记录会出现空洞
// （模型点了、失败了，而记录里什么都没有）。所以这里的几个函数都是围绕
// 「一次动作 = 一条事件」这一件事，不掺调度逻辑。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/store"
)

// NowOffset 当前时刻相对录像起点的毫秒数（还没有录像时返回 -1）。
//
// 批次要用它圈定"这一段连续操作"的起止：一批一条录像，不是每步一条。
// 记录里每一步的偏移量也走这里——偏移量只有这一个算法，别处不要自己减。
func (m *Manager) NowOffset(id string) int64 {
	live, ok := m.liveOf(id)
	if !ok || live.recordingStartMs <= 0 {
		return -1
	}
	if offset := time.Now().UnixMilli() - live.recordingStartMs; offset > 0 {
		return offset
	}
	return 0
}

// SetBatchMeta 给 [fromSeq, toSeq] 这一段补上批次起止与意图。
func (m *Manager) SetBatchMeta(ctx context.Context, id string, fromSeq, toSeq int, startMS, endMS int64, intent string) error {
	if fromSeq > toSeq {
		return nil
	}
	return m.store.SetBatchMeta(ctx, id, fromSeq, toSeq, startMS, endMS, intent)
}

// Touch 记录一次活动，顺延空闲回收时间点。
func (m *Manager) Touch(id string) {
	if live, ok := m.liveOf(id); ok {
		m.mu.Lock()
		live.lastActivity = time.Now()
		m.mu.Unlock()
	}
}

// Describe 取引用号对应的元素（供上层把「已点击 e5」写成元素名）。
func (m *Manager) Describe(insp Inspector, ref string) (inspect.Element, bool) {
	describer, ok := insp.(describeElement)
	if !ok || ref == "" {
		return inspect.Element{}, false
	}
	return describer.Describe(ref)
}

// inspector 取（必要时重建）检查器。
func (m *Manager) inspector(ctx context.Context, id string) (Inspector, error) {
	live, ok := m.liveOf(id)
	if !ok {
		return nil, fmt.Errorf("会话 %s 不在运行中", id)
	}
	if live.inspector != nil {
		return live.inspector, nil
	}
	insp, err := m.factory(ctx, live.session.CDPPort)
	if err != nil {
		return nil, fmt.Errorf("连接调试端口失败: %w", err)
	}
	m.mu.Lock()
	live.inspector = insp
	m.mu.Unlock()
	return insp, nil
}

// Recorded 一次工具调用在记录里留下的东西。
//
// 用一个结构体而不是 (Step, string, string, error)：两个并列的 string 谁是谁
// 全靠记，传反了编译器也不会拦。capture 要用 Step.Shot（那张图就是回给模型的那张），
// 其余动作只关心 Result。
type Recorded struct {
	// Step 记完的那一步（Caption / Shot 已经填好）
	Step Step
	// Result 这一步的结果摘要：成功时是"已点击「保存」按钮"，失败时是错误信息
	Result string
	// Artifact 产出的文件（相对会话目录），一般没有
	Artifact string
}

// Action 一次动作的执行体。
//
// step 是可以回填的：有些信息只有执行时才知道（按文字定位到的引用号、实际点中的元素），
// 回填之后 Record 才能给它配上截图高亮和"点击「保存」按钮"这样的说明。
// 回填不是改写记录：工具名、参数、意图在第一个人手里就定了。
type Action func(ctx context.Context, insp Inspector, step *Step) (result string, artifact string, err error)

// Record 执行一次工具调用并写审计：参数、结果摘要、耗时、在录像里的偏移量。
//
// 所有对外动作都必须走这里，否则审计会出现空洞——那正是这个项目最不能接受的。
// 注意 fn 里的任何失败（包括"按文字找不到元素"这种还没发出点击的失败）都会以
// ok=false 进记录：读者要看到的是"模型想点那个按钮，但页面上没有"。
func (m *Manager) Record(ctx context.Context, id string, step Step, fn Action) (Recorded, error) {
	insp, err := m.inspector(ctx, id)
	if err != nil {
		step.Caption = captionFor(step, nil)
		m.writeEvent(ctx, id, step, eventOutcome{Result: err.Error(), OffsetMS: -1})
		return Recorded{Step: step}, err
	}

	started := time.Now()
	result, artifact, callErr := fn(ctx, insp, &step)
	// CDP 连接会因为应用重载、DevTools 断开而失效；这时重连一次再试，
	// 而不是让这个会话之后所有操作都报 "use of closed network connection"。
	if callErr != nil && isTransportError(callErr) {
		m.log.Warn("检查器连接已失效，重连后重试", "session", id, "err", callErr)
		m.dropInspector(id)
		if fresh, err := m.inspector(ctx, id); err == nil {
			insp = fresh
			result, artifact, callErr = fn(ctx, insp, &step)
		}
	}
	duration := time.Since(started)
	offset := m.touchAndOffset(id, started)

	msg := result
	if callErr != nil {
		msg = callErr.Error()
	}

	// 画面在动作之后截：要拍到的是「动作的结果」，不是动作之前的样子。
	if step.Caption == "" {
		step.Caption = captionFor(step, describe(insp, step))
	}
	ok := callErr == nil
	// 审计不能因为 ctx 被取消就丢：超时、客户端断开正是最需要留下记录的时刻。
	// （之前这里用原 ctx，一步超时后连"这一步失败了"都写不进去。）
	auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer auditCancel()

	// 只给"需要看现场"的步骤配图：失败的，以及 capture 这种"这一步的价值就是那张图"的。
	// 其余每步都截一张的话，一批 30 步光截图就多花好几秒，而记录里并不逐行展示它们。
	if (!ok || step.AlwaysShot) && step.Shot == "" {
		if sess, err := m.Get(auditCtx, id); err == nil && sess.Dir != "" {
			step.Shot = m.captureShot(auditCtx, id, sess.Dir, insp, step)
		}
	}
	m.writeEvent(auditCtx, id, step, eventOutcome{
		Result: msg, Artifact: artifact, DurationMS: duration.Milliseconds(), OK: ok, OffsetMS: offset,
	})

	// 应用可能在动作的副作用里自己改窗口（登录后切全屏、从大窗缩回默认尺寸）。
	// 记在动作之后：记录里的顺序就是「点了什么 → 应用因此变成了什么样」。
	m.syncFrame(ctx, id, insp)

	if callErr != nil {
		return Recorded{Step: step, Artifact: artifact}, callErr
	}
	return Recorded{Step: step, Result: result, Artifact: artifact}, nil
}

// touchAndOffset 记一次活动，并算出这一步在录像里的位置。
//
// 偏移量按"动作开始的那一刻"算，不是按写记录的时刻：截图和落库要花几百毫秒，
// 用写记录的时间会把整条记录往后拖。
func (m *Manager) touchAndOffset(id string, started time.Time) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	live, ok := m.live[id]
	if !ok {
		return -1
	}
	live.lastActivity = time.Now()
	if live.recordingStartMs <= 0 {
		return -1
	}
	if offset := started.UnixMilli() - live.recordingStartMs; offset > 0 {
		return offset
	}
	return 0
}

// eventOutcome 一条审计里"发生了什么"的部分；其余字段（工具、参数、说明）从 Step 来。
//
// 单独一个结构体是因为这几个值都长得差不多："" 和 "0" 挨在一起传，
// 顺序错一个编译器也不会拦（当初把 result 和 artifact 传反过）。
type eventOutcome struct {
	// Result 给读者看的结果摘要（失败时是错误信息）
	Result string
	// Artifact 这一步产出的文件（相对会话目录），一般没有
	Artifact string
	// DurationMS 这一步花了多久
	DurationMS int64
	// OK 这一步成功与否
	OK bool
	// OffsetMS 在录像里的位置（毫秒，-1 表示没有录像）
	OffsetMS int64
}

func (m *Manager) writeEvent(ctx context.Context, id string, step Step, out eventOutcome) {
	rawArgs, err := json.Marshal(step.Args)
	if err != nil {
		rawArgs = []byte(`{}`)
	}
	caption := step.Caption
	if caption == "" {
		caption = captionFor(step, nil)
	}
	// 记下写这条记录时窗口的矩形：记录按它裁剪片段，窗口中途变过也不会错位
	frame := store.Rect{}
	if live, ok := m.liveOf(id); ok {
		frame = live.session.Frame
	}

	// 审计是旁路：写失败只告警，不影响工具本身的成败
	if _, err := m.store.AppendEvent(ctx, store.Event{
		SessionID: id, At: time.Now(), Tool: step.Tool, Args: rawArgs, OK: out.OK, Kind: step.Kind,
		Caption: caption, Intent: step.Intent, Expect: step.Expect,
		DurationMS: out.DurationMS, VideoOffsetMS: out.OffsetMS, Result: truncate(out.Result, 2000),
		ArtifactPath: out.Artifact, Shot: step.Shot, Frame: frame,
	}); err != nil {
		m.log.Error("写审计事件失败", "session", id, "tool", step.Tool, "err", err)
	}
}
