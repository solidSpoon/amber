package store

// feedback.go：AI 用完工作流后留下的建议。
//
// 为什么单独存：它不是某次验收的证据，而是对产品本身的意见。
// 混进 events 会让报告出现"这步在抱怨工具"这种噪音，也没法单独拿出来分析。

import (
	"context"
	"fmt"
	"time"
)

// Feedback 一条建议。
type Feedback struct {
	ID        int64
	At        time.Time
	SessionID string
	Source    string
	Rating    int
	Body      string
}

// AddFeedback 记一条建议。
func (s *Store) AddFeedback(ctx context.Context, f Feedback) (int64, error) {
	if f.Source == "" {
		f.Source = "ai"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO feedback (at, session_id, source, rating, body) VALUES (?, ?, ?, ?, ?)`,
		f.At.UTC().Format(time.RFC3339Nano), f.SessionID, f.Source, f.Rating, f.Body)
	if err != nil {
		return 0, fmt.Errorf("写入建议失败: %w", err)
	}
	return res.LastInsertId()
}

// ListFeedback 最近的建议，新的在前。
func (s *Store) ListFeedback(ctx context.Context, limit int) ([]Feedback, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, session_id, source, rating, body FROM feedback ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取建议失败: %w", err)
	}
	defer rows.Close()

	out := make([]Feedback, 0, limit)
	for rows.Next() {
		var f Feedback
		var at string
		if err := rows.Scan(&f.ID, &at, &f.SessionID, &f.Source, &f.Rating, &f.Body); err != nil {
			return nil, fmt.Errorf("扫描建议失败: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			f.At = t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
