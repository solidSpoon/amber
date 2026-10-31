package session

// machines.go：看板视图——「每台机器现在是谁在用、用了多少」。
//
// 这是唯一一处把会话状态翻译成"人看的机器"的地方：槽位、占用者、容器资源。
// 它只读，不改任何会话状态。

import (
	"context"
	"time"

	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/store"
)

// MachineView 看板上的一台机器：固定槽位 + 当前占用它的会话（可能没有）。
type MachineView struct {
	Slot      int           `json:"slot"`
	Occupied  bool          `json:"occupied"`
	SessionID string        `json:"sessionId,omitempty"`
	App       string        `json:"app,omitempty"`
	Title     string        `json:"title,omitempty"`
	Ref       string        `json:"ref,omitempty"`
	Status    string        `json:"status,omitempty"`
	Since     time.Time     `json:"since,omitempty"`
	IdleUntil time.Time     `json:"idleUntil,omitempty"`
	CDPPort   int           `json:"cdpPort,omitempty"`
	VNCPort   int           `json:"vncPort,omitempty"`
	Stats     dockerx.Stats `json:"stats"`
	StatsOK   bool          `json:"statsOk"`
}

// Machines 返回全部槽位状态（看板首页的数据源）。
//
// 统计数字走 docker stats，单次调用要几十到几百毫秒，所以做 3 秒缓存：
// 看板本来就是"看个大概"，没必要每次刷新都把 docker 敲一遍。
func (m *Manager) Machines(ctx context.Context) ([]MachineView, error) {
	used, err := m.store.UsedSlots(ctx)
	if err != nil {
		return nil, err
	}

	// 用一次查询把活跃会话取出来，避免逐槽位查库
	active, err := m.store.ActiveSessions(ctx)
	if err != nil {
		return nil, err
	}
	bySlot := make(map[int]store.Session, len(active))
	for _, sess := range active {
		bySlot[sess.Slot] = sess
	}

	out := make([]MachineView, 0, m.cfg.MaxSessions)
	for slot := 0; slot < m.cfg.MaxSessions; slot++ {
		view := MachineView{Slot: slot}
		if sessID, ok := used[slot]; ok {
			sess := bySlot[slot]
			view.Occupied = true
			view.SessionID = sessID
			view.App = sess.App
			view.Title = sess.Title
			view.Ref = sess.Ref
			view.Status = string(sess.Status)
			view.Since = sess.StartedAt
			view.IdleUntil = sess.IdleDeadline
			view.CDPPort = sess.CDPPort
			view.VNCPort = sess.VNCPort
			if stats, ok := m.cachedStats(sess.Container); ok {
				view.Stats = stats
				view.StatsOK = true
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// Preview 取会话的一帧预览（不写审计：看板刷屏不应污染验收记录）。
func (m *Manager) Preview(ctx context.Context, id string, quality int) ([]byte, error) {
	return m.ScreenJPEG(ctx, id, previewMaxWidth)
}

// cachedStats 3 秒内的容器统计直接复用。
func (m *Manager) cachedStats(container string) (dockerx.Stats, bool) {
	if container == "" {
		return dockerx.Stats{}, false
	}
	m.statsMu.Lock()
	if cached, ok := m.statsCache[container]; ok && time.Since(cached.at) < 3*time.Second {
		m.statsMu.Unlock()
		return cached.stats, true
	}
	m.statsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stats, err := m.docker.Stats(ctx, container)
	if err != nil {
		return dockerx.Stats{}, false
	}
	m.statsMu.Lock()
	if m.statsCache == nil {
		m.statsCache = map[string]statsEntry{}
	}
	m.statsCache[container] = statsEntry{stats: stats, at: time.Now()}
	m.statsMu.Unlock()
	return stats, true
}

type statsEntry struct {
	stats dockerx.Stats
	at    time.Time
}
