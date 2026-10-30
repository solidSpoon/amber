// 会话操作接口：把管理台/脚本能做的动作暴露成 HTTP。
//
// 与 MCP 复用同一套 Manager.Record，所以无论从哪条路进来，审计与录像偏移的行为完全一致。
// 这些接口主要用途：调试、脚本化验证、以及后续管理台里的"手动接管"。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/solidSpoon/rigd/internal/inspect"
	"github.com/solidSpoon/rigd/internal/session"
)

type snapshotResponse struct {
	URL      string            `json:"url"`
	Title    string            `json:"title"`
	Elements []inspect.Element `json:"elements"`
	Text     string            `json:"text"`
}

func (s *Server) actionSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var snap inspect.Snapshot
	_, _, err := s.manager.Record(r.Context(), id, "snapshot", map[string]any{},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			got, err := insp.Snapshot(ctx)
			if err != nil {
				return "", "", err
			}
			snap = got
			return inspect.Summarize(got), "", nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshotResponse{URL: snap.URL, Title: snap.Title, Elements: snap.Elements, Text: snap.Text})
}

type screenshotRequest struct {
	Label string `json:"label"`
}

func (s *Server) actionScreenshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req screenshotRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
			return
		}
	}
	label := sanitize(req.Label)

	var rel string
	_, artifact, err := s.manager.Record(r.Context(), id, "screenshot", map[string]any{"label": req.Label},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			sess, err := s.manager.Get(ctx, id)
			if err != nil {
				return "", "", err
			}
			rel = filepath.Join("screenshots", fmt.Sprintf("%s-%s.png", time.Now().Format("150405"), label))
			n, err := insp.Screenshot(ctx, filepath.Join(sess.Dir, rel))
			if err != nil {
				return "", "", err
			}
			return fmt.Sprintf("截图已保存：%s（%d 字节）", rel, n), rel, nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path": artifact,
		"url":  fmt.Sprintf("/api/sessions/%s/artifacts/%s", id, filepath.Base(rel)),
	})
}

type evalRequest struct {
	Script string `json:"script"`
}

func (s *Server) actionEval(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req evalRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.Script) == "" {
		writeError(w, http.StatusBadRequest, errors.New("script 不能为空"))
		return
	}

	var out json.RawMessage
	_, _, err := s.manager.Record(r.Context(), id, "eval", map[string]any{"script": req.Script},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			raw, err := insp.Evaluate(ctx, req.Script)
			if err != nil {
				return "", "", err
			}
			out = raw
			return string(raw), "", nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": out})
}

type clickRequest struct {
	Ref string  `json:"ref"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
}

func (s *Server) actionClick(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req clickRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	if req.Ref == "" && (req.X == 0 && req.Y == 0) {
		writeError(w, http.StatusBadRequest, errors.New("ref 与 x/y 至少要给一个"))
		return
	}
	msg, _, err := s.manager.Record(r.Context(), id, "click",
		map[string]any{"ref": req.Ref, "x": req.X, "y": req.Y},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			if req.Ref != "" {
				if err := insp.Click(ctx, req.Ref); err != nil {
					return "", "", err
				}
				return "已点击 " + req.Ref, "", nil
			}
			if err := insp.ClickAt(ctx, req.X, req.Y); err != nil {
				return "", "", err
			}
			return fmt.Sprintf("已点击 (%.0f, %.0f)", req.X, req.Y), "", nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: msg})
}

type fillRequest struct {
	Ref    string `json:"ref"`
	Text   string `json:"text"`
	Submit bool   `json:"submit"`
}

func (s *Server) actionFill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req fillRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	msg, _, err := s.manager.Record(r.Context(), id, "fill",
		map[string]any{"ref": req.Ref, "text": req.Text, "submit": req.Submit},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			if err := insp.Fill(ctx, req.Ref, req.Text); err != nil {
				return "", "", err
			}
			if req.Submit {
				if err := insp.Press(ctx, "Enter"); err != nil {
					return "", "", err
				}
			}
			return fmt.Sprintf("已填入 %q", req.Text), "", nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: msg})
}

type pressRequest struct {
	Key string `json:"key"`
}

func (s *Server) actionPress(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req pressRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	msg, _, err := s.manager.Record(r.Context(), id, "press_key", map[string]any{"key": req.Key},
		func(ctx context.Context, insp session.Inspector) (string, string, error) {
			if err := insp.Press(ctx, req.Key); err != nil {
				return "", "", err
			}
			return "已按键 " + req.Key, "", nil
		})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{OK: true, Message: msg})
}

type actionResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// sanitize 把标签变成安全文件名片段。
func sanitize(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "shot"
	}
	out := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-").Replace(label)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
