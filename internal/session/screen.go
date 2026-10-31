package session

// screen.go：整屏截图——证据是整块屏幕，不是网页那一层。
//
// 为什么不用 CDP 的 Page.captureScreenshot：它只拍页面。原生文件对话框、
// 系统菜单、窗口边框全都拍不到，而验收时最需要看明白的往往正是那些"看不见的窗口"。
// 这里改成在容器里对虚拟屏抓一帧：录屏抓的也是这块屏，图和录像才是同一个现场。
//
// 抓帧走容器里现成的 ffmpeg（录屏用的就是它），不额外引依赖；
// 产物落到会话目录的 /out（宿主可读），再从宿主读回来——不拿二进制走 docker exec 的文本管道。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// shotMaxWidth 记录里步骤图的宽度上限：够看清字幕和按钮，体积又不至于失控。
	shotMaxWidth = 1600
	// previewMaxWidth 回给模型的预览帧：手机上也能看清，不必传一张 1920 的大图。
	previewMaxWidth = 1120
)

// ScreenJPEG 抓一帧整屏 JPEG（不落盘，调用方用完就丢）。
func (m *Manager) ScreenJPEG(ctx context.Context, id string, maxWidth int) ([]byte, error) {
	path, _, err := m.grabScreen(ctx, id, "frame", maxWidth, false)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取整屏帧失败: %w", err)
	}
	return raw, nil
}

// SaveScreenJPEG 抓一帧整屏 JPEG 并留在会话目录里，返回相对路径（记录用它当这一步的画面）。
func (m *Manager) SaveScreenJPEG(ctx context.Context, id, prefix string, maxWidth int) (string, error) {
	_, rel, err := m.grabScreen(ctx, id, prefix, maxWidth, true)
	return rel, err
}

// grabScreen 在容器里对虚拟屏抓一帧，写到会话目录的 shots/ 下。
//
// keep=false 时文件名带 tmp- 前缀，由调用方读走字节后自行删除。
func (m *Manager) grabScreen(ctx context.Context, id, prefix string, maxWidth int, keep bool) (string, string, error) {
	m.mu.Lock()
	live, ok := m.live[id]
	m.mu.Unlock()
	if !ok || live.session.Container == "" {
		return "", "", fmt.Errorf("会话 %s 不在运行中", id)
	}
	if maxWidth <= 0 {
		maxWidth = shotMaxWidth
	}
	width, height := m.cfg.Display.Width, m.cfg.Display.Height
	if d := live.session.Display; d.Width > 0 && d.Height > 0 {
		width, height = d.Width, d.Height
	}

	name := fmt.Sprintf("%s-%s.jpg", time.Now().Format("150405.000"), sanitizeTool(prefix))
	if !keep {
		name = "tmp-" + name
	}
	rel := filepath.Join("shots", name)
	if err := os.MkdirAll(filepath.Join(live.session.Dir, "shots"), 0o755); err != nil {
		return "", "", fmt.Errorf("创建截图目录失败: %w", err)
	}

	cmd := []string{
		"env", fmt.Sprintf("DISPLAY=:%d", m.displayOf(live.session.Slot)),
		"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "x11grab",
		"-video_size", fmt.Sprintf("%dx%d", width, height),
		"-i", fmt.Sprintf(":%d", m.displayOf(live.session.Slot)),
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxWidth),
		"-q:v", "3", "-y",
		filepath.Join("/out", rel),
	}
	if _, err := m.docker.Exec(ctx, live.session.Container, cmd...); err != nil {
		return "", "", fmt.Errorf("整屏抓帧失败: %w", err)
	}

	path := filepath.Join(live.session.Dir, rel)
	if _, err := os.Stat(path); err != nil {
		return "", "", fmt.Errorf("整屏抓帧没有产出文件: %w", err)
	}
	return path, rel, nil
}
