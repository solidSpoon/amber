// Package store 是控制面唯一的持久化层：会话、事件（审计）、产物索引、指标采样。
//
// 选型说明：SQLite + 纯 Go 驱动（modernc.org/sqlite），不依赖 cgo，交叉编译和容器镜像都简单；
// 迁移用内嵌 SQL 文件顺序执行，测试里可以对同一个库重复跑（幂等），避免出现「只在生产炸」。
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go sqlite 驱动，注册为 "sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Status 是会话状态机。调度器只允许按这个图迁移：
//
//	starting → ready → stopping → finished
//	     ↓         ↓         ↓
//	   failed  abandoned  failed
type Status string

const (
	// StatusStarting 容器正在创建/应用正在构建。
	StatusStarting Status = "starting"
	// StatusReady 应用已起来、CDP 可达、录屏已开始，可以接受工具调用。
	StatusReady Status = "ready"
	// StatusStopping 正在收尾（停录屏、归档产物）。
	StatusStopping Status = "stopping"
	// StatusFinished 正常收尾完成。
	StatusFinished Status = "finished"
	// StatusFailed 启动或运行期间失败。
	StatusFailed Status = "failed"
	// StatusAbandoned 超时被回收（客户端可能已经断了）。
	StatusAbandoned Status = "abandoned"
)

// Terminal 判断状态是否已经结束（不再需要调度器照看）。
func (s Status) Terminal() bool {
	switch s {
	case StatusFinished, StatusFailed, StatusAbandoned:
		return true
	}
	return false
}

// Rect 画面几何（像素）。X/Y 是左上角在虚拟屏里的位置，Width/Height 是有内容的尺寸。
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Known 判断几何是否有效。
func (r Rect) Known() bool { return r.Width > 0 && r.Height > 0 }

// Session 一次验收会话（= 一台一次性测试机）。
type Session struct {
	ID        string
	App       string
	Ref       string
	CommitSHA string
	Image     string
	Container string
	Status    Status
	CDPPort   int
	VNCPort   int
	// Slot 占用的机器槽位（0..N-1），看板按槽位固定展示两台机器
	Slot int
	Dir  string
	// Payload 被测产物的来源（dir:/… 或 manifest:sha256:…）
	Payload string
	// Assets 素材槽位 → 来源（JSON），槽位由产物的 rig.json 声明
	Assets string
	// Recipe 产物自述（rig.json 的原文快照），报告里能看出当时怎么启动的
	Recipe string
	// Frame 录像里真正有内容的那块区域（= 应用窗口），零值表示未知。
	// Display 它所在的虚拟屏；两者不同就说明录进去的桌面比窗口大。
	Frame           Rect
	Display         Rect
	CreatedAt       time.Time
	StartedAt       time.Time
	EndedAt         time.Time
	IdleDeadline    time.Time
	VideoPath       string
	VideoDurationMS int64
	Verdict         string
	Summary         string
	Error           string
}

// Event 一次工具调用（审计记录的最小单元）。
type Event struct {
	ID        int64
	SessionID string
	Seq       int
	At        time.Time
	Tool      string
	Args      json.RawMessage
	OK        bool
	// Kind 这一步的性质：action（做了件事）/ assert（断言）/ note（只写说明）。
	Kind string
	// Caption 动作的人话描述，从可访问性树自动生成（点击「保存」按钮）。
	Caption string
	// Intent 为什么做这一步；Expect 期望看到什么。都由调用方给，可空。
	Intent        string
	Expect        string
	DurationMS    int64
	VideoOffsetMS int64
	Result        string
	ArtifactPath  string
	// Shot 这一步的画面（相对会话目录）；失败也截，报告里最有用的就是失败那张。
	Shot string
	// Frame 写这条记录时窗口的矩形。窗口会在会话中途变，裁剪必须按"当时"的几何来。
	Frame Rect
	// BatchStartMS / BatchEndMS 这条记录所属那段连续操作的起止（相对录像起点）。
	// 切片按这段来：一批一条录像，而不是每步一条。
	BatchStartMS int64
	BatchEndMS   int64
}

// Artifact 会话产物（截图/录像/报告）。
type Artifact struct {
	ID        int64
	SessionID string
	Kind      string
	Path      string
	Bytes     int64
	CreatedAt time.Time
}

// Sample 一次系统指标采样。
type Sample struct {
	At         time.Time
	CPUPct     float64
	MemUsed    uint64
	MemTotal   uint64
	DiskUsed   uint64
	DiskTotal  uint64
	Containers int
}

// Store 封装数据库。所有方法都接受 context，便于 HTTP 请求取消时及时退出。
type Store struct {
	db *sql.DB
	// blobRoot 内容寻址存储的根目录（<data>/blobs）；与 DB 同在一个数据目录下。
	blobRoot string
}

// Open 打开（不存在则创建）数据库并执行迁移。
//
// dsn 为 ":memory:" 时用于测试；此时连接池被限制为 1，避免每个连接拿到各自的空库。
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	// sqlite 的并发写入需要显式等待锁，WAL 让读写互不阻塞
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("设置 %s 失败: %w", p, err)
		}
	}

	blobRoot := filepath.Join(filepath.Dir(dsn), "blobs")
	if dsn == ":memory:" || strings.HasPrefix(dsn, "file:") {
		// 内存库/临时库：放到系统临时目录，测试之间互不影响
		root, err := os.MkdirTemp("", "rigd-blobs-")
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		blobRoot = root
	}
	if err := os.MkdirAll(filepath.Join(blobRoot, "tmp"), 0o755); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("创建 blob 目录失败: %w", err)
	}

	s := &Store{db: db, blobRoot: blobRoot}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// migrate 按文件名顺序执行内嵌迁移，已执行过的跳过（记录在 schema_migrations）。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("创建迁移表失败: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("读取迁移目录失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, name).Scan(&exists)
		if err != nil {
			return fmt.Errorf("查询迁移 %s 失败: %w", name, err)
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("读取迁移 %s 失败: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("开启迁移事务失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("执行迁移 %s 失败: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, now()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("记录迁移 %s 失败: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交迁移 %s 失败: %w", name, err)
		}
	}
	return nil
}

// now 统一时间格式：sqlite 里存 RFC3339（UTC），排序与比较都是字符串序。
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// formatTime 空时间写 NULL，避免出现 0001-01-01 这种伪值。
func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// CreateSession 写入一条新会话。
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(id, app, ref, commit_sha, image, container, status, cdp_port, vnc_port, slot, dir,
		 payload, assets, recipe,
		 frame_x, frame_y, frame_w, frame_h, display_w, display_h,
		 created_at, started_at, ended_at, idle_deadline, video_path, video_duration_ms, verdict, summary, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.App, sess.Ref, sess.CommitSHA, sess.Image, sess.Container, string(sess.Status),
		sess.CDPPort, sess.VNCPort, sess.Slot, sess.Dir,
		sess.Payload, sess.Assets, sess.Recipe,
		sess.Frame.X, sess.Frame.Y, sess.Frame.Width, sess.Frame.Height, sess.Display.Width, sess.Display.Height,
		sess.CreatedAt.UTC().Format(time.RFC3339Nano), formatTime(sess.StartedAt), formatTime(sess.EndedAt),
		formatTime(sess.IdleDeadline), sess.VideoPath, sess.VideoDurationMS, sess.Verdict, sess.Summary, sess.Error)
	if err != nil {
		return fmt.Errorf("写入会话失败: %w", err)
	}
	return nil
}

// UpdateSession 更新可变字段；零值字段不会被覆盖（用指针语义太重，这里显式列出可变列）。
func (s *Store) UpdateSession(ctx context.Context, sess Session) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET
		container = ?, status = ?, cdp_port = ?, vnc_port = ?,
		payload = ?, assets = ?, recipe = ?,
		frame_x = ?, frame_y = ?, frame_w = ?, frame_h = ?, display_w = ?, display_h = ?,
		started_at = ?, ended_at = ?, idle_deadline = ?,
		video_path = ?, video_duration_ms = ?, verdict = ?, summary = ?, error = ?, commit_sha = ?
		WHERE id = ?`,
		sess.Container, string(sess.Status), sess.CDPPort, sess.VNCPort,
		sess.Payload, sess.Assets, sess.Recipe,
		sess.Frame.X, sess.Frame.Y, sess.Frame.Width, sess.Frame.Height, sess.Display.Width, sess.Display.Height,
		formatTime(sess.StartedAt), formatTime(sess.EndedAt), formatTime(sess.IdleDeadline),
		sess.VideoPath, sess.VideoDurationMS, sess.Verdict, sess.Summary, sess.Error, sess.CommitSHA, sess.ID)
	if err != nil {
		return fmt.Errorf("更新会话失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("记录不存在")

const sessionColumns = `id, app, ref, commit_sha, image, container, status, cdp_port, vnc_port, slot, dir,
	payload, assets, recipe,
	frame_x, frame_y, frame_w, frame_h, display_w, display_h,
	created_at, started_at, ended_at, idle_deadline, video_path, video_duration_ms, verdict, summary, error`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var (
		sess                             Session
		status                           string
		createdAt                        string
		startedAt, endedAt, idleDeadline sql.NullString
	)
	err := sc.Scan(&sess.ID, &sess.App, &sess.Ref, &sess.CommitSHA, &sess.Image, &sess.Container, &status,
		&sess.CDPPort, &sess.VNCPort, &sess.Slot, &sess.Dir,
		&sess.Payload, &sess.Assets, &sess.Recipe,
		&sess.Frame.X, &sess.Frame.Y, &sess.Frame.Width, &sess.Frame.Height,
		&sess.Display.Width, &sess.Display.Height,
		&createdAt, &startedAt, &endedAt, &idleDeadline,
		&sess.VideoPath, &sess.VideoDurationMS, &sess.Verdict, &sess.Summary, &sess.Error)
	if err != nil {
		return Session{}, err
	}
	sess.Status = Status(status)
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		sess.CreatedAt = t
	}
	sess.StartedAt = parseTime(startedAt)
	sess.EndedAt = parseTime(endedAt)
	sess.IdleDeadline = parseTime(idleDeadline)
	return sess, nil
}

// GetSession 按 ID 取会话。
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("读取会话失败: %w", err)
	}
	return sess, nil
}

// ListSessions 按创建时间倒序返回会话，limit<=0 表示不限。
func (s *Store) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	q := `SELECT ` + sessionColumns + ` FROM sessions ORDER BY created_at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("列出会话失败: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0, 16)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描会话失败: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// AppendEvent 追加一条审计事件，并返回分配到的序号。
//
// 序号在会话内单调递增，前端据此稳定排序；录像偏移量由调用方算好传进来。
func (s *Store) AppendEvent(ctx context.Context, e Event) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?`, e.SessionID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("计算事件序号失败: %w", err)
	}
	args := string(e.Args)
	if args == "" {
		args = "{}"
	}
	ok := 0
	if e.OK {
		ok = 1
	}
	if e.Kind == "" {
		e.Kind = "action"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events
		(session_id, seq, at, tool, args, ok, kind, caption, intent, expect, duration_ms, video_offset_ms, result, artifact_path, shot,
		 frame_x, frame_y, frame_w, frame_h, batch_start_ms, batch_end_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SessionID, seq, e.At.UTC().Format(time.RFC3339Nano), e.Tool, args, ok,
		e.Kind, e.Caption, e.Intent, e.Expect,
		e.DurationMS, e.VideoOffsetMS, e.Result, e.ArtifactPath, e.Shot,
		e.Frame.X, e.Frame.Y, e.Frame.Width, e.Frame.Height,
		orDefault(e.BatchStartMS), orDefault(e.BatchEndMS)); err != nil {
		return 0, fmt.Errorf("写入事件失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交事件失败: %w", err)
	}
	return seq, nil
}

// SetBatchWindow 给一段连续操作（一次 run 调用）补上起止位置。
//
// 批次是切片的单位：一批一条录像，从第一步前 2 秒到最后一步后 2 秒。
// 起止要等这一批跑完才知道，所以先写事件、结束后再补。
func (s *Store) SetBatchWindow(ctx context.Context, sessionID string, fromSeq, toSeq int, startMS, endMS int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET batch_start_ms = ?, batch_end_ms = ?
		WHERE session_id = ? AND seq BETWEEN ? AND ?`, startMS, endMS, sessionID, fromSeq, toSeq)
	if err != nil {
		return fmt.Errorf("写入批次区间失败: %w", err)
	}
	return nil
}

func orDefault(v int64) int64 {
	return v
}

// ListEvents 返回会话的全部事件，按 seq 升序。
func (s *Store) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, seq, at, tool, args, ok,
		kind, caption, intent, expect, duration_ms, video_offset_ms, result, artifact_path, shot,
		frame_x, frame_y, frame_w, frame_h, batch_start_ms, batch_end_ms
		FROM events WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询事件失败: %w", err)
	}
	defer rows.Close()

	out := make([]Event, 0, 32)
	for rows.Next() {
		var (
			e    Event
			at   string
			args string
			ok   int
		)
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &at, &e.Tool, &args, &ok,
			&e.Kind, &e.Caption, &e.Intent, &e.Expect,
			&e.DurationMS, &e.VideoOffsetMS, &e.Result, &e.ArtifactPath, &e.Shot,
			&e.Frame.X, &e.Frame.Y, &e.Frame.Width, &e.Frame.Height,
			&e.BatchStartMS, &e.BatchEndMS); err != nil {
			return nil, fmt.Errorf("扫描事件失败: %w", err)
		}
		e.Args = json.RawMessage(args)
		e.OK = ok == 1
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			e.At = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddArtifact 记录一个产物文件。
func (s *Store) AddArtifact(ctx context.Context, a Artifact) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts (session_id, kind, path, bytes, created_at)
		VALUES (?, ?, ?, ?, ?)`, a.SessionID, a.Kind, a.Path, a.Bytes, a.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("写入产物失败: %w", err)
	}
	return nil
}

// ListArtifacts 返回会话产物索引。
func (s *Store) ListArtifacts(ctx context.Context, sessionID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, kind, path, bytes, created_at
		FROM artifacts WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询产物失败: %w", err)
	}
	defer rows.Close()

	out := make([]Artifact, 0, 8)
	for rows.Next() {
		var (
			a         Artifact
			createdAt string
		)
		if err := rows.Scan(&a.ID, &a.SessionID, &a.Kind, &a.Path, &a.Bytes, &createdAt); err != nil {
			return nil, fmt.Errorf("扫描产物失败: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			a.CreatedAt = t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InsertSample 写入一条指标采样。
func (s *Store) InsertSample(ctx context.Context, m Sample) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO samples
		(at, cpu_pct, mem_used, mem_total, disk_used, disk_total, containers)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.At.UTC().Format(time.RFC3339Nano), m.CPUPct, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal, m.Containers)
	if err != nil {
		return fmt.Errorf("写入指标失败: %w", err)
	}
	return nil
}

// RecentSamples 返回最近 limit 条采样，按时间升序（方便直接画曲线）。
func (s *Store) RecentSamples(ctx context.Context, limit int) ([]Sample, error) {
	if limit <= 0 {
		limit = 120
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, cpu_pct, mem_used, mem_total, disk_used, disk_total, containers
		FROM samples ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询指标失败: %w", err)
	}
	defer rows.Close()

	out := make([]Sample, 0, limit)
	for rows.Next() {
		var (
			m  Sample
			at string
		)
		if err := rows.Scan(&at, &m.CPUPct, &m.MemUsed, &m.MemTotal, &m.DiskUsed, &m.DiskTotal, &m.Containers); err != nil {
			return nil, fmt.Errorf("扫描指标失败: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			m.At = t
		}
		out = append(out, m)
	}
	// 倒序取最近 N 条后再翻转，返回升序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// UsedSlots 返回当前被非终态会话占用的槽位集合。
func (s *Store) UsedSlots(ctx context.Context) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slot, id FROM sessions
		WHERE status NOT IN (?, ?, ?)`, string(StatusFinished), string(StatusFailed), string(StatusAbandoned))
	if err != nil {
		return nil, fmt.Errorf("查询槽位占用失败: %w", err)
	}
	defer rows.Close()

	used := map[int]string{}
	for rows.Next() {
		var (
			slot int
			id   string
		)
		if err := rows.Scan(&slot, &id); err != nil {
			return nil, fmt.Errorf("扫描槽位失败: %w", err)
		}
		used[slot] = id
	}
	return used, rows.Err()
}

// ActiveSessions 返回所有非终态会话，供调度器做超时回收。
func (s *Store) ActiveSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions
		WHERE status NOT IN (?, ?, ?) ORDER BY created_at`, string(StatusFinished), string(StatusFailed), string(StatusAbandoned))
	if err != nil {
		return nil, fmt.Errorf("查询活跃会话失败: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0, 4)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描会话失败: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}
