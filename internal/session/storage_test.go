package session

// storage_test.go：存储统计与"整体清空"。
//
// 清空是不可逆的，所以这里最要紧的一条不是"删得干净"，而是"只删自己该删的"：
// 会话目录是库里的绝对路径，库一旦被复制（备份、试跑、换机器），
// 那些行指向的就是另一个数据目录——照着删就是删别人的数据。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solidSpoon/rigd/internal/store"
)

// 清空会删画面、抹掉事件里的引用，但会话与时间线留着。
func TestPurgeMediaKeepsRecord(t *testing.T) {
	ctx := context.Background()
	m, st, _, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{Payload: "dir:" + writeTestPayload(t, "dashplayer"), Title: "清空测试"})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	// 造一份母片、一条片段、一张截图（真实会话里由容器和收尾流程产生）
	mustWrite(t, filepath.Join(sess.Dir, "video", "session.mp4"), []byte("mp4-master"))
	mustWrite(t, filepath.Join(sess.Dir, "clips", "batch-00000000.mp4"), []byte("clip"))
	shot := "shots/1-capture.jpg"
	mustWrite(t, filepath.Join(sess.Dir, shot), []byte("jpeg"))
	if _, err := st.AppendEvent(ctx, store.Event{SessionID: sess.ID, At: time.Now(), Tool: "capture", Shot: shot}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}

	if _, err := m.Stop(ctx, sess.ID); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	// 收尾会把母片搬进 archive/ 并导出时间线
	if _, err := os.Stat(filepath.Join(sess.Dir, "archive", "session.mp4")); err != nil {
		t.Fatalf("母片应归档到 archive/：%v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.Dir, "archive", "timeline.json")); err != nil {
		t.Fatalf("应导出时间线：%v", err)
	}

	result, err := m.PurgeMedia(ctx)
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if result.Sessions != 1 || result.FreedBytes <= 0 {
		t.Fatalf("清空结果不对: %+v", result)
	}

	for _, gone := range []string{"archive/session.mp4", "clips", "shots"} {
		if _, err := os.Stat(filepath.Join(sess.Dir, gone)); err == nil {
			t.Fatalf("%s 应被删掉", gone)
		}
	}
	// 记录与时间线必须还在
	if _, err := os.Stat(filepath.Join(sess.Dir, "archive", "timeline.json")); err != nil {
		t.Fatalf("时间线不该被清掉：%v", err)
	}
	events, err := st.ListEvents(ctx, sess.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("事件不该被删：%v %v", events, err)
	}
	for _, e := range events {
		if e.Shot != "" {
			t.Fatalf("事件的画面引用应被抹掉: %+v", e)
		}
	}
	if got, err := st.GetSession(ctx, sess.ID); err != nil || !got.Status.Terminal() {
		t.Fatalf("会话应保持已结束: %+v %v", got, err)
	}
}

// 目录不在本数据目录下的会话，一个字节都不能碰。
//
// 场景很真实：库被复制/恢复过来（备份、试跑、换机器），行里的 dir 还是原来那台机器上的
// 绝对路径。照着它删，删的就是别人的会话。
func TestPurgeSkipsForeignSessions(t *testing.T) {
	ctx := context.Background()
	m, st, _, _ := newTestManager(t)

	foreign := t.TempDir()
	mustWrite(t, filepath.Join(foreign, "clips", "batch-00000001.mp4"), []byte("别删我"))
	if err := st.CreateSession(ctx, store.Session{
		ID:        "foreign-1",
		App:       "别人的应用",
		Status:    store.StatusFinished,
		Dir:       foreign,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("写会话失败: %v", err)
	}

	result, err := m.PurgeMedia(ctx)
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if result.Skipped != 1 || result.Sessions != 0 {
		t.Fatalf("外来会话应被跳过: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(foreign, "clips", "batch-00000001.mp4")); err != nil {
		t.Fatalf("外来目录被删了：%v", err)
	}
}

// 还在跑的会话跳过：它的母片正在写，删了只会得到半截文件。
func TestPurgeSkipsRunningSession(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := newTestManager(t)
	if _, err := m.Start(ctx, Options{Payload: "dir:" + writeTestPayload(t, "dashplayer")}); err != nil {
		t.Fatalf("起会话失败: %v", err)
	}
	result, err := m.PurgeMedia(ctx)
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if result.Skipped != 1 || result.Sessions != 0 {
		t.Fatalf("运行中的会话应被跳过: %+v", result)
	}
}

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

// 时间线的偏移量以"录像真正开始"为原点，并且把那个时刻本身写进去。
//
// 这一点容易搞错：会话创建时间、容器就绪时间都比开录早，
// 用它们当原点，所有偏移量都会整体前移。
func TestTimelineAnchorsAtRecordingStart(t *testing.T) {
	ctx := context.Background()
	m, st, _, _ := newTestManager(t)
	sess, err := m.Start(ctx, Options{Payload: "dir:" + writeTestPayload(t, "dashplayer")})
	if err != nil {
		t.Fatalf("起会话失败: %v", err)
	}

	// 容器开录时刻：比会话创建晚 3 秒
	recordingStart := time.Now().Add(-3 * time.Second).UnixMilli()
	sess.RecordingStartMs = recordingStart
	sess.VideoDurationMS = 12_000
	if err := st.UpdateSession(ctx, sess); err != nil {
		t.Fatalf("写会话失败: %v", err)
	}
	if _, err := st.AppendEvent(ctx, store.Event{
		SessionID: sess.ID, At: time.Now(), Tool: "capture", Caption: "看一眼", OK: true, VideoOffsetMS: 1_500,
	}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}

	if err := m.writeTimeline(ctx, sess); err != nil {
		t.Fatalf("导出时间线失败: %v", err)
	}
	raw, err := os.ReadFile(timelinePath(sess))
	if err != nil {
		t.Fatalf("读时间线失败: %v", err)
	}
	var got Timeline
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析时间线失败: %v", err)
	}
	if got.RecordingStartMs != recordingStart {
		t.Fatalf("原点 = %d，期望 %d", got.RecordingStartMs, recordingStart)
	}
	if got.RecordingStartedAt == "" {
		t.Fatal("应当给出录像起点的墙钟时间")
	}
	if len(got.Steps) != 1 || got.Steps[0].OffsetMS != 1_500 {
		t.Fatalf("步骤偏移不对: %+v", got.Steps)
	}
}
