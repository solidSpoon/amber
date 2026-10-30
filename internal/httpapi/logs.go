// 会话日志：把"应用为什么没起来/为什么白屏"直接摆到台面上。
//
// 排查体验的痛点在于：容器是一次性的，出问题时容器已经没了，docker logs 也取不到。
// 所以调度器会在删除容器前把 stdout/stderr 归档到会话目录；这里把归档文件、应用自身日志
// 和录屏/X 的日志统一列出来，前端做成可切换的日志面板。
package httpapi

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// logSource 一种可查看的日志来源。
type logSource struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
}

// resolveLogPath 把来源名解析成具体文件；返回 ok=false 表示该来源当前没有日志。
//
// 只允许白名单来源，路径全部由服务端拼，不接受客户端传任意路径。
func resolveLogPath(sessionDir, name string) (string, bool) {
	switch name {
	case "container":
		return existing(filepath.Join(sessionDir, "logs", "container.log"))
	case "ffmpeg":
		return existing(filepath.Join(sessionDir, "logs", "ffmpeg.log"))
	case "xvfb":
		return existing(filepath.Join(sessionDir, "logs", "xvfb.log"))
	case "openbox":
		return existing(filepath.Join(sessionDir, "logs", "openbox.log"))
	case "app":
		// 应用自身日志：开发态落在 userData/logs-dev，正式态在 logs
		for _, dir := range []string{
			filepath.Join(sessionDir, "userdata", "logs-dev"),
			filepath.Join(sessionDir, "userdata", "logs"),
		} {
			// 应用日志是 jsonl（main-YYYY-MM-DD.jsonl），别的模块可能写 .log，两种都认
			for _, suffix := range []string{".jsonl", ".log"} {
				if newest, ok := newestFile(dir, suffix); ok {
					return newest, true
				}
			}
		}
	}
	return "", false
}

func existing(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", false
	}
	return path, true
}

// newestFile 取目录下最新的匹配文件（应用日志按天/按启动分段，最新那份才有意义）。
func newestFile(dir, suffix string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var best string
	var bestTime int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		if info.ModTime().UnixNano() > bestTime {
			bestTime = info.ModTime().UnixNano()
			best = filepath.Join(dir, entry.Name())
		}
	}
	return best, best != ""
}

var logCatalog = []struct{ name, label string }{
	{"container", "容器输出"},
	{"app", "应用日志"},
	{"ffmpeg", "录屏"},
	{"xvfb", "虚拟屏"},
	{"openbox", "窗口管理器"},
}

// listLogs 返回该会话有哪些日志可看。
func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	sources := make([]logSource, 0, len(logCatalog))
	for _, item := range logCatalog {
		path, ok := resolveLogPath(sess.Dir, item.name)
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		sources = append(sources, logSource{Name: item.name, Label: item.label, Bytes: info.Size()})
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

// getLog 返回某个来源的日志尾部（纯文本），tail 指定行数。
func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	sess, err := s.manager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	path, ok := resolveLogPath(sess.Dir, chi.URLParam(r, "source"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("该来源暂无日志"))
		return
	}
	tail := 300
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			tail = n
		}
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = file.Close() }()

	text, err := tailLines(file, tail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

// tailLines 从文件读最后 n 行：日志文件可能几百 MB，不能整个读进内存。
func tailLines(file *os.File, n int) (string, error) {
	const chunk = 64 * 1024
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	offset := size
	var collected []byte
	for offset > 0 && countLines(collected) <= n {
		readSize := int64(chunk)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		buf := make([]byte, readSize)
		if _, err := file.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		collected = append(buf, collected...)
	}
	lines := strings.Split(string(collected), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func countLines(b []byte) int {
	return strings.Count(string(b), "\n")
}
