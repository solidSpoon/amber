package session

// clip.go：录像片段——记录里"这一段的画面"从哪来。
//
// 两种画面，用途不同：
//
//	母片  archive/session.mp4  整场录像，永久留着。它是原始现场：AI 事后要重新取帧、
//	                           核对某一秒到底发生了什么，只有它能回答。页面不放它（没人
//	                           从头看一遍），但存储页能看到它占多少、可以整体清空。
//	片段  clips/batch-*.mp4    每一批那几秒，收尾时从母片切好。记录里就看这个：小、秒开。
//
// 切片单位是"一段连续操作"（一次 run 调用），不是单步：一批里的相邻两步
// 可能只隔 0.2 秒，每步各切前后 2 秒会把同一段画面重复十几遍。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/solidSpoon/rigd/internal/store"
)

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

// masterVideo 整场录像（母片）的路径。
//
// 容器录在 video/ 下（RIG_OUT=/out 的约定）；收尾时整个搬到 archive/，
// 那里是"归档区"，存储页按它统计、清空也清它。会话还在跑时读 video/。
func masterVideo(sess store.Session) string {
	if info, err := os.Stat(archiveVideo(sess)); err == nil && info.Size() > 0 {
		return archiveVideo(sess)
	}
	return filepath.Join(sess.Dir, "video", "session.mp4")
}

// archiveVideo 归档后的母片位置。
func archiveVideo(sess store.Session) string {
	return filepath.Join(sess.Dir, "archive", "session.mp4")
}

// timelinePath 收尾导出的"带时间戳的步骤清单"。
func timelinePath(sess store.Session) string {
	return filepath.Join(sess.Dir, "archive", "timeline.json")
}

// clipPath 这一步的片段落在哪：同一批共用一个文件，按批起点命名。
func clipPath(sess store.Session, target store.Event) string {
	return filepath.Join(sess.Dir, clipName(clipKey(target)))
}

// Clip 返回某一步所在那一段的短视频路径（没有缓存就现切）。
//
// 正常路径是「直接命中缓存」：收尾时已经切好。现切服务两种情况——
// 会话还在跑（文档里点开某一步），或者存储页把片段清空了但母片还在。
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
// 两者都要看：片段被清掉了但母片还在时，点开还能现切一段；都清掉了才真的没有。
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
