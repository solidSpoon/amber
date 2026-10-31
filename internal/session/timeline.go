package session

// timeline.go：收尾时导出的「带时间戳的步骤清单」。
//
// 为什么要有这个文件：记录页是给人看的，而 AI 事后想"把第 40 秒那两秒重新切出来看看"
// 时，需要的是机器可读的东西——每一步发生了什么、在母片的哪个毫秒、对应哪张截图。
// 这些事实本来就在 events 表里，这里只是把它们投影成一份文件，和母片放在一起：
//
//	archive/session.mp4    整场录像
//	archive/timeline.json  每一步的时间戳与说明
//
// 导出成文件而不是只留库里的表，是因为下游工具（ffmpeg、脚本、别的 agent）
// 拿到一个路径就能干活，不用反过来理解我们的数据库和 REST 形状。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/solidSpoon/rigd/internal/store"
)

// Timeline 一份会话的机器可读时间线。
type Timeline struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title,omitempty"`
	App       string `json:"app"`
	Ref       string `json:"ref,omitempty"`
	CreatedAt string `json:"createdAt"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	// About 写清这份文件怎么读：拿到它的多半是另一个 AI。
	About string `json:"about"`
	// Video 母片在会话目录里的相对路径（清空存储后这个文件就不在了）
	Video string `json:"video,omitempty"`
	// DurationMS 母片总长
	DurationMS int64 `json:"durationMs"`
	// Display / Frame 录像的整幅像素、以及其中应用窗口那一块
	Display *store.Rect    `json:"display,omitempty"`
	Frame   *store.Rect    `json:"frame,omitempty"`
	Steps   []TimelineStep `json:"steps"`
}

// TimelineStep 时间线里的一步：记录里的一步 + 它在母片里的位置。
type TimelineStep struct {
	Seq int `json:"seq"`
	// At 这一步发生的墙钟时间（RFC3339Nano）
	At string `json:"at"`
	// OffsetMS 相对母片起点的毫秒偏移；-1 表示录像还没开始（这段时间不在视频里）
	OffsetMS int64  `json:"offsetMs"`
	Tool     string `json:"tool"`
	Caption  string `json:"caption"`
	Intent   string `json:"intent,omitempty"`
	OK       bool   `json:"ok"`
	// BatchStartMS / BatchEndMS 这一步所属那段连续操作的起止（相对母片起点）
	BatchStartMS int64 `json:"batchStartMs,omitempty"`
	BatchEndMS   int64 `json:"batchEndMs,omitempty"`
	// Clip 这一段对应的片段（相对会话目录）：同一批共用一条，已按批次切好
	Clip string `json:"clip,omitempty"`
	// Shot 这一步的截图（相对会话目录；只有失败步骤和 capture 才有）
	Shot string `json:"shot,omitempty"`
}

const timelineAbout = "offsetMs 是相对 video 起点的毫秒偏移，直接喂给 ffmpeg -ss 即可取这一段；" +
	"同一批操作的几步共用一条 clip（batchStartMs 相同）；shot 是整屏截图。" +
	"清空存储后 video/clip/shot 指向的文件会不存在，但这份清单本身保留。"

// writeTimeline 把事件导出到 <会话目录>/archive/timeline.json。
//
// 失败只告警不报错：时间线是"额外的方便"，母片和记录本身已经完整了。
func (m *Manager) writeTimeline(ctx context.Context, sess store.Session) error {
	events, err := m.store.ListEvents(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("读取事件失败: %w", err)
	}

	timeline := Timeline{
		SessionID:  sess.ID,
		Title:      sess.Title,
		App:        sess.App,
		Ref:        sess.Ref,
		CreatedAt:  rfc3339(sess.CreatedAt),
		StartedAt:  rfc3339(sess.StartedAt),
		EndedAt:    rfc3339(sess.EndedAt),
		About:      timelineAbout,
		DurationMS: sess.VideoDurationMS,
		Steps:      make([]TimelineStep, 0, len(events)),
	}
	if _, err := os.Stat(archiveVideo(sess)); err == nil {
		timeline.Video = filepath.Join("archive", "session.mp4")
	}
	if sess.Display.Known() {
		display := sess.Display
		timeline.Display = &display
	}
	if sess.Frame.Known() {
		frame := sess.Frame
		timeline.Frame = &frame
	}

	for _, event := range events {
		step := TimelineStep{
			Seq: event.Seq, At: rfc3339(event.At), OffsetMS: event.VideoOffsetMS,
			Tool: event.Tool, Caption: event.Caption, Intent: event.Intent, OK: event.OK,
			BatchStartMS: event.BatchStartMS, BatchEndMS: event.BatchEndMS,
			Shot: event.Shot,
		}
		// 只有真的要得出画面才写片段路径：没有偏移量的步骤（录像开始前）也没有片段
		if event.VideoOffsetMS >= 0 && HasVisual(event.Tool) {
			step.Clip = clipName(clipKey(event))
		}
		timeline.Steps = append(timeline.Steps, step)
	}

	if err := os.MkdirAll(filepath.Dir(timelinePath(sess)), 0o755); err != nil {
		return fmt.Errorf("创建归档目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(timeline, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化时间线失败: %w", err)
	}
	if err := os.WriteFile(timelinePath(sess), append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入时间线失败: %w", err)
	}
	return nil
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// clipKey 是片段命名用的键：同一批共用一条，按批次起点命名（没批次就按自身偏移）。
func clipKey(event store.Event) int64 {
	if event.BatchStartMS >= 0 {
		return event.BatchStartMS
	}
	return event.VideoOffsetMS
}
