package session

// storage.go：磁盘上到底占了什么，以及"整体清空"。
//
// 记录本身很小（文字 + 时间戳），占地方的是画面：每个会话一份母片、一批一条片段、
// 失败步骤和 capture 的整屏截图。母片是有意留着的（AI 事后要重新取帧），
// 所以得给一个能看见、能一次性收走的地方——而不是让用户去翻目录。
//
// 只有整体清空，没有逐条删除：这条工具里"删掉某个会话的画面"没有意义
// （记录还在，画面没了，读者会更糊涂），要收就整批收走。

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solidSpoon/rigd/internal/store"
)

// StorageSession 存储页上的一行：一个会话占了多少。
type StorageSession struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// EndedAt 收尾时间；会话还在跑时为零
	EndedAt string `json:"endedAt,omitempty"`
	// Running 还在跑：它的母片正在写，清空时会被跳过
	Running    bool  `json:"running"`
	VideoBytes int64 `json:"videoBytes"`
	ClipBytes  int64 `json:"clipBytes"`
	ShotBytes  int64 `json:"shotBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// StorageTotals 合计。
type StorageTotals struct {
	Sessions   int   `json:"sessions"`
	VideoBytes int64 `json:"videoBytes"`
	ClipBytes  int64 `json:"clipBytes"`
	ShotBytes  int64 `json:"shotBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// StorageUsage 存储页的数据。
type StorageUsage struct {
	Sessions []StorageSession `json:"sessions"`
	Totals   StorageTotals    `json:"totals"`
	// Blobs 内容寻址库（产物与素材）：不归这里管，按 TTL 自动回收，只在页面上报个数
	Blobs struct {
		Count int   `json:"count"`
		Bytes int64 `json:"bytes"`
	} `json:"blobs"`
}

// Storage 统计每个会话的母片/片段/截图占用。
func (m *Manager) Storage(ctx context.Context) (StorageUsage, error) {
	sessions, err := m.store.ListSessions(ctx, 0)
	if err != nil {
		return StorageUsage{}, err
	}

	usage := StorageUsage{Sessions: make([]StorageSession, 0, len(sessions))}
	for _, sess := range sessions {
		if !m.ownsDir(sess) {
			continue
		}
		row := StorageSession{
			ID:         sess.ID,
			Title:      sess.Title,
			Running:    !sess.Status.Terminal(),
			VideoBytes: dirBytes([]string{filepath.Dir(archiveVideo(sess)), filepath.Join(sess.Dir, "video")}, "session.mp4"),
			ClipBytes:  dirBytes([]string{filepath.Join(sess.Dir, "clips")}, ""),
			ShotBytes:  dirBytes([]string{filepath.Join(sess.Dir, "shots"), filepath.Join(sess.Dir, "screenshots")}, ""),
		}
		row.TotalBytes = row.VideoBytes + row.ClipBytes + row.ShotBytes
		if !sess.EndedAt.IsZero() {
			row.EndedAt = rfc3339(sess.EndedAt)
		}
		usage.Totals.Sessions++
		usage.Totals.VideoBytes += row.VideoBytes
		usage.Totals.ClipBytes += row.ClipBytes
		usage.Totals.ShotBytes += row.ShotBytes
		usage.Sessions = append(usage.Sessions, row)
	}
	usage.Totals.TotalBytes = usage.Totals.VideoBytes + usage.Totals.ClipBytes + usage.Totals.ShotBytes
	// 占得多的排前面：这一页是"谁在吃磁盘"，不是时间线
	sort.SliceStable(usage.Sessions, func(i, j int) bool {
		return usage.Sessions[i].TotalBytes > usage.Sessions[j].TotalBytes
	})

	count, bytes, err := m.store.BlobUsage(ctx)
	if err != nil {
		return usage, err
	}
	usage.Blobs.Count, usage.Blobs.Bytes = count, bytes
	return usage, nil
}

// PurgeResult 整体清空的结果。
type PurgeResult struct {
	// FreedBytes 真正从盘上收回来的字节数
	FreedBytes int64 `json:"freedBytes"`
	// Sessions 被清掉画面的会话数
	Sessions int `json:"sessions"`
	// Skipped 还在跑、因此被跳过的会话数
	Skipped int `json:"skipped"`
}

// PurgeMedia 删掉所有会话的画面（母片、片段、截图），保留会话与步骤记录。
//
// 在跑的会话跳过：它的母片正在写，删了只会得到一个半截文件。
// 清完把事件里对画面的引用一起抹掉，记录页就不会挂一堆打不开的图。
func (m *Manager) PurgeMedia(ctx context.Context) (PurgeResult, error) {
	sessions, err := m.store.ListSessions(ctx, 0)
	if err != nil {
		return PurgeResult{}, err
	}

	var result PurgeResult
	var failed []error
	for _, sess := range sessions {
		if !sess.Status.Terminal() {
			result.Skipped++
			continue
		}
		// 目录不在本实例的数据目录下就跳过：库是可能被复制过来的（备份、试跑），
		// 那些行里的绝对路径指向别人的会话——照着删就是删别人的数据。
		if !m.ownsDir(sess) {
			m.log.Warn("跳过一个不属于本数据目录的会话", "session", sess.ID, "dir", sess.Dir)
			result.Skipped++
			continue
		}
		removed, err := purgeSessionMedia(sess)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if removed > 0 {
			result.Sessions++
			result.FreedBytes += removed
		}
	}

	if _, err := m.store.ClearMediaRefs(ctx); err != nil {
		failed = append(failed, err)
	}
	if err := errors.Join(failed...); err != nil {
		return result, err
	}
	m.log.Info("已清空会话画面", "sessions", result.Sessions, "freed_mb", result.FreedBytes>>20, "skipped", result.Skipped)
	return result, nil
}

// ownsDir 判断这个会话的目录是否在本实例的 sessions 根目录下。
//
// 所有"按 sess.Dir 删东西"的地方都要先过这一关：会话目录是库里的绝对路径，
// 而库可以被复制/恢复，复制过来的行指的还是原来那台机器上的路径。
func (m *Manager) ownsDir(sess store.Session) bool {
	root := m.cfg.SessionsDir()
	rel, err := filepath.Rel(root, sess.Dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

// purgeSessionMedia 删掉一个会话的画面文件，返回释放的字节数。
//
// 时间线（archive/timeline.json）留着：它只有几 KB，而且正是"画面曾经存在过、
// 每一步发生在第几毫秒"的记录，删了反而让记录变成一段没头没尾的文字。
func purgeSessionMedia(sess store.Session) (int64, error) {
	var freed int64
	var failed []error

	// 母片：归档区和录制区的都算（老会话没有归档）
	for _, path := range []string{archiveVideo(sess), filepath.Join(sess.Dir, "video", "session.mp4")} {
		size, err := removeFile(path)
		freed += size
		if err != nil {
			failed = append(failed, err)
		}
	}
	// 片段与截图：整个目录收走（里面只有画面）
	for _, dir := range []string{
		filepath.Join(sess.Dir, "clips"),
		filepath.Join(sess.Dir, "shots"),
		filepath.Join(sess.Dir, "screenshots"),
	} {
		freed += dirBytes([]string{dir}, "")
		if err := os.RemoveAll(dir); err != nil {
			failed = append(failed, err)
		}
	}
	return freed, errors.Join(failed...)
}

// removeFile 删一个文件并返回它的大小（不存在算成功）。
func removeFile(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := os.Remove(path); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// dirBytes 统计若干目录里文件的总大小。
//
// only 非空时只统计这个文件名（用来把 archive/ 里的 timeline.json 排除在"录像"之外）。
func dirBytes(dirs []string, only string) int64 {
	var total int64
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if only != "" && entry.Name() != only {
				return nil
			}
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
			return nil
		})
	}
	return total
}
