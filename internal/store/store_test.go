package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// newTestStore 起一个内存库并确保用完关闭。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleSession(id string) Session {
	return Session{
		ID:        id,
		App:       "DashPlayer",
		Ref:       "main",
		CommitSHA: "9da4ce76",
		Image:     "electron-acceptance-rig:latest",
		Status:    StatusStarting,
		CDPPort:   9222,
		VNCPort:   6080,
		Dir:       "/tmp/rigd/" + id,
		CreatedAt: time.Now(),
	}
}

// 会话写入后能读回，且时间字段不丢失。
func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	sess := sampleSession("s-1")
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}

	sess.Status = StatusReady
	sess.StartedAt = time.Now()
	sess.VideoPath = "/tmp/rigd/s-1/video/session.mp4"
	if err := s.UpdateSession(ctx, sess); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}

	got, err := s.GetSession(ctx, "s-1")
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got.Status != StatusReady {
		t.Fatalf("状态应为 ready，实际 %q", got.Status)
	}
	if got.VideoPath != sess.VideoPath {
		t.Fatalf("录像路径未持久化: %q", got.VideoPath)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("开始时间丢失")
	}
}

// 查询不存在的会话要返回 ErrNotFound，让上层能区分"没有"和"出错"。
func TestGetSessionNotFound(t *testing.T) {
	if _, err := newTestStore(t).GetSession(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}

// 事件序号必须由存储层分配且严格递增，前端排序依赖这一点。
func TestAppendEventAssignsIncreasingSeq(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateSession(ctx, sampleSession("s-2")); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}

	for _, tool := range []string{"snapshot", "click", "screenshot"} {
		if _, err := s.AppendEvent(ctx, Event{
			SessionID: "s-2", At: time.Now(), Tool: tool,
			Args: json.RawMessage(`{"x":1}`), OK: true, DurationMS: 12, VideoOffsetMS: 1500,
		}); err != nil {
			t.Fatalf("追加事件失败: %v", err)
		}
	}

	events, err := s.ListEvents(ctx, "s-2")
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("期望 3 条事件，实际 %d", len(events))
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Fatalf("第 %d 条序号应为 %d，实际 %d", i, i+1, e.Seq)
		}
		if !e.OK || e.VideoOffsetMS != 1500 {
			t.Fatalf("事件字段丢失: %+v", e)
		}
	}
	if events[2].Tool != "screenshot" {
		t.Fatalf("事件顺序不对: %s", events[2].Tool)
	}
}

// 列表按创建时间倒序，最近的在最前。
func TestListSessionsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	older := sampleSession("s-old")
	older.CreatedAt = time.Now().Add(-time.Hour)
	newer := sampleSession("s-new")
	newer.CreatedAt = time.Now()

	for _, sess := range []Session{older, newer} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
	}
	list, err := s.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if len(list) != 2 || list[0].ID != "s-new" {
		t.Fatalf("排序不对: %+v", list)
	}
}

// 指标采样取"最近 N 条"后按时间升序返回，前端可以直接画折线。
func TestRecentSamplesAscendingWindow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	base := time.Now().Add(-time.Minute)

	for i := 0; i < 5; i++ {
		if err := s.InsertSample(ctx, Sample{
			At: base.Add(time.Duration(i) * time.Second), CPUPct: float64(i),
			MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, Containers: i,
		}); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}

	got, err := s.RecentSamples(ctx, 3)
	if err != nil {
		t.Fatalf("查询指标失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d", len(got))
	}
	if got[0].CPUPct != 2 || got[2].CPUPct != 4 {
		t.Fatalf("窗口或顺序不对: %+v", got)
	}
	if !got[0].At.Before(got[1].At) {
		t.Fatal("应为升序")
	}
}

// 活跃会话只返回非终态，调度器靠它扫超时；终态会话不能被反复回收。
func TestActiveSessionsExcludesTerminal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ready := sampleSession("s-ready")
	done := sampleSession("s-done")
	done.Status = StatusFinished

	for _, sess := range []Session{ready, done} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
	}

	active, err := s.ActiveSessions(ctx)
	if err != nil {
		t.Fatalf("查询活跃会话失败: %v", err)
	}
	if len(active) != 1 || active[0].ID != "s-ready" {
		t.Fatalf("活跃会话集合不对: %+v", active)
	}
}

// 迁移必须幂等：对着同一个库重复开、重复跑迁移不能报错，也不能重复建表。
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("查询迁移记录失败: %v", err)
	}
	// 每个内嵌迁移恰好记录一次（当前是 0001_init + 0002_session_slot）
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("读取迁移目录失败: %v", err)
	}
	if count != len(entries) {
		t.Fatalf("迁移记录数应等于迁移文件数 %d，实际 %d", len(entries), count)
	}
}
