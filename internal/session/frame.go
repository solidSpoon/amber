package session

// frame.go：画面几何——「录像里哪一块是应用窗口」。
//
// 为什么需要这一层：
// 录屏抓的是虚拟屏的根窗口（这样弹窗、菜单、原生对话框都不会漏），但应用窗口
// 不一定会铺满虚拟屏。窗口只占一角时，整段录像就是「一个小窗 + 一大片空桌面」，
// 既看不清细节，也没法当证据用。
//
// 所以链路上有两道保证：
//  1. 起会话时在容器里把窗口铺满（rig-fit-window），并把窗口矩形写进 logs/window.json；
//  2. 控制面把这份几何存进会话，界面按它裁剪显示；万一没铺满，录像也会退化成只抓窗口区域。
//
// 对旧版本录的像（没有几何元数据），读取时用 ffmpeg 从像素里量一次并回填——只量一次，
// 之后走库里的数字。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/solidSpoon/rigd/internal/store"
)

// windowMeta 是容器内 rig-fit-window 写的 logs/window.json。
type windowMeta struct {
	Display struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"display"`
	Window struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"window"`
	Filled bool `json:"filled"`
}

// fitWindow 在容器里把应用窗口铺满虚拟屏。
//
// 走容器内的 WM（wmctrl），不走 CDP：Browser 域只挂在浏览器端点上，
// 从页面目标发 Browser.getWindowForTarget 会直接报 "wasn't found"。
// 顺带的好处是与应用无关——Tauri、Qt、GTK 的应用同样适用。
func (m *Manager) fitWindow(ctx context.Context, container string) error {
	display := fmt.Sprintf("DISPLAY=:%d", m.cfg.Display.Num)
	if _, err := m.docker.Exec(ctx, container, "env", display, "RIG_OUT=/out", "rig-fit-window"); err != nil {
		return err
	}
	return nil
}

// readWindowGeometry 读容器写的画面几何。读不到不算错误——旧镜像没有这一步。
func (m *Manager) readWindowGeometry(ctx context.Context, container string) (store.Rect, store.Rect, error) {
	out, err := m.docker.Exec(ctx, container, "cat", "/out/logs/window.json")
	if err != nil {
		return store.Rect{}, store.Rect{}, err
	}
	var meta windowMeta
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		return store.Rect{}, store.Rect{}, fmt.Errorf("解析 window.json 失败: %w", err)
	}
	display := store.Rect{Width: meta.Display.Width, Height: meta.Display.Height}
	frame := store.Rect{
		X: meta.Window.X, Y: meta.Window.Y,
		Width: meta.Window.Width, Height: meta.Window.Height,
	}
	if !display.Known() {
		return store.Rect{}, store.Rect{}, errors.New("window.json 缺少虚拟屏尺寸")
	}
	if !frame.Known() {
		return store.Rect{}, store.Rect{}, errors.New("window.json 缺少窗口尺寸")
	}
	return frame, display, nil
}

// waitContainerReady 等容器把「画面取景定下来 + 录屏真的开始了」这件事做完。
//
// 光有 CDP 不等于能验收：应用窗口可能还在 960x540，录屏也还没开。
// 容器在 ready.json 落盘之后才打印 READY，所以以这个文件为准。
func (m *Manager) waitContainerReady(ctx context.Context, container string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.docker.Exec(ctx, container, "cat", "/out/logs/ready.json"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("容器 30s 内没有报告就绪（window.json / ready.json 未出现）")
}

// EnsureFrame 保证会话的画面几何已知。
//
// 新会话在就绪时就已经有几何（容器量过）；旧会话没有元数据，这里从录像像素里量一次
// 并写回库——之后的请求都直接读库，不会再跑 ffmpeg。
//
// 量不出来不是错误：界面退化成「按整幅画面显示」，只是没那么好看。
func (m *Manager) EnsureFrame(ctx context.Context, id string) (store.Session, error) {
	sess, err := m.Get(ctx, id)
	if err != nil {
		return sess, err
	}
	// 只有已经收尾的会话需要补：运行中的会话几何由容器给，不可能缺
	if sess.Frame.Known() || !sess.Status.Terminal() || sess.VideoPath == "" {
		return sess, nil
	}
	if _, busy := m.frameInflight.LoadOrStore(id, true); busy {
		return sess, nil
	}
	defer m.frameInflight.Delete(id)

	frame, display, err := detectVideoFrame(ctx, sess.VideoPath)
	if err != nil {
		m.log.Warn("从录像量画面区域失败，界面将按整幅画面显示", "session", id, "err", err)
		return sess, nil
	}
	sess.Frame = frame
	sess.Display = display
	if err := m.store.UpdateSession(ctx, sess); err != nil {
		m.log.Warn("回填画面几何失败", "session", id, "err", err)
	}
	return sess, nil
}

// detectVideoFrame 用 ffmpeg 量出录像里"有内容"的区域。
//
// 取巧点：cropdetect 找的是「非黑的边」，而桌面底色通常是浅色，直接用它会把白底
// 当成"内容"。先 negate 把浅色底变成黑，cropdetect 量的就是应用窗口的包围盒。
// 只解关键帧（-skip_frame nokey），160s 的 1080p 录像大约 0.1s 出结果。
func detectVideoFrame(ctx context.Context, path string) (store.Rect, store.Rect, error) {
	display, err := probeVideoSize(ctx, path)
	if err != nil {
		return store.Rect{}, store.Rect{}, err
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "info",
		"-skip_frame", "nokey", "-i", path,
		"-vf", "negate,cropdetect=limit=24:round=2:reset=0",
		"-frames:v", "40", "-f", "null", "-")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return store.Rect{}, store.Rect{}, fmt.Errorf("调用 ffmpeg 失败: %w", err)
	}

	frame, ok := parseCropBox(string(raw), display)
	if !ok {
		return store.Rect{}, store.Rect{}, errors.New("录像里没量出画面区域")
	}
	return frame, display, nil
}

// probeVideoSize 取录像像素尺寸（= 录制时虚拟屏的尺寸）。
func probeVideoSize(ctx context.Context, path string) (store.Rect, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		return store.Rect{}, fmt.Errorf("ffprobe 读取录像尺寸失败: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 2 {
		return store.Rect{}, fmt.Errorf("无法解析录像尺寸: %q", strings.TrimSpace(string(out)))
	}
	w, errW := strconv.Atoi(parts[0])
	h, errH := strconv.Atoi(parts[1])
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return store.Rect{}, fmt.Errorf("无法解析录像尺寸: %q", strings.TrimSpace(string(out)))
	}
	return store.Rect{Width: w, Height: h}, nil
}

// parseCropBox 从 cropdetect 的输出里取所有候选矩形，合并成并集。
//
// 取并集而不是最后一帧：整段录像里应用可能临时缩小（弹窗、切页面），
// 并集才是"这段录像覆盖到的最大有效区域"。
func parseCropBox(output string, display store.Rect) (store.Rect, bool) {
	var (
		minX, minY = display.Width, display.Height
		maxX, maxY int
		found      bool
	)
	for _, field := range strings.Fields(output) {
		if !strings.HasPrefix(field, "crop=") {
			continue
		}
		var w, h, x, y int
		if _, err := fmt.Sscanf(strings.TrimPrefix(field, "crop="), "%d:%d:%d:%d", &w, &h, &x, &y); err != nil {
			continue
		}
		if w <= 0 || h <= 0 {
			continue
		}
		found = true
		if x < minX {
			minX = x
		}
		if y < minY {
			minY = y
		}
		if x+w > maxX {
			maxX = x + w
		}
		if y+h > maxY {
			maxY = y + h
		}
	}
	if !found {
		return store.Rect{}, false
	}

	frame := store.Rect{X: minX, Y: minY, Width: maxX - minX, Height: maxY - minY}
	// 贴合整幅画面时统一记成「铺满」：cropdetect 会因为一两行纯色边把框收进去几个像素，
	// 那不是真的裁切，不该让界面做无意义的缩放。
	if frame.Width >= display.Width*98/100 && frame.Height >= display.Height*98/100 {
		return store.Rect{Width: display.Width, Height: display.Height}, true
	}
	if frame.Width < 32 || frame.Height < 32 {
		return store.Rect{}, false
	}
	return frame, true
}

// Poster 返回录像首帧静帧的路径；没有就抽一张并缓存到会话目录。
//
// 抽帧位置取「时长的 10%，最多 5 秒」：太靠前可能是启动瞬间的空窗，
// 太靠后又浪费解复用时间。抽不出来不算错误之外的异常，调用方按 404 处理。
func (m *Manager) Poster(ctx context.Context, id string) (string, error) {
	sess, err := m.Get(ctx, id)
	if err != nil {
		return "", err
	}
	video := sess.VideoPath
	if video == "" {
		video = filepath.Join(sess.Dir, "video", "session.mp4")
	}
	if info, err := os.Stat(video); err != nil || info.Size() == 0 {
		return "", errors.New("该会话还没有录像")
	}

	poster := filepath.Join(sess.Dir, "poster.jpg")
	if info, err := os.Stat(poster); err == nil && info.Size() > 0 {
		return poster, nil
	}

	offset := 5.0
	if sess.VideoDurationMS > 0 {
		offset = math.Min(float64(sess.VideoDurationMS)/1000/10, 5)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(offset, 'f', 2, 64), "-i", video,
		"-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", "-q:v", "4", poster)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("截取封面失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if info, err := os.Stat(poster); err != nil || info.Size() == 0 {
		return "", errors.New("封装文件里没有可用的帧")
	}
	return poster, nil
}
