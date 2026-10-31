package session

// desktop.go：X 层的键盘输入。
//
// 为什么需要它：原生文件对话框、系统菜单不在页面里，CDP 的输入事件到不了。
// 但它仍然是"一次动作对应一步"：调用方给的是具体按键或一段文字，
// 报告里写的是「按下 ctrl+l」「输入 /library」，不是一段没人看得懂的脚本。

import (
	"context"
	"fmt"
)

// DesktopKey 在 X 层发一个组合键（如 "ctrl+l"、"Return"）。
func (m *Manager) DesktopKey(ctx context.Context, id, combo string) error {
	return m.xdotool(ctx, id, "key", "--clearmodifiers", combo)
}

// DesktopMove 把鼠标移到屏幕坐标上（真实指针轨迹）。
//
// 有些悬停交互只认真实指针：CDP 的合成 mouseMoved 到不了 React 的 hover 状态。
func (m *Manager) DesktopMove(ctx context.Context, id string, x, y int) error {
	return m.xdotool(ctx, id, "mousemove", "--sync", fmt.Sprint(x), fmt.Sprint(y))
}

// DesktopType 在 X 层输入一段文字。
func (m *Manager) DesktopType(ctx context.Context, id, text string) error {
	return m.xdotool(ctx, id, "type", "--delay", "20", text)
}

func (m *Manager) xdotool(ctx context.Context, id string, args ...string) error {
	m.mu.Lock()
	live, ok := m.live[id]
	m.mu.Unlock()
	if !ok || live.session.Container == "" {
		return fmt.Errorf("会话 %s 不在运行中", id)
	}
	display := fmt.Sprintf("DISPLAY=:%d", m.cfg.Display.Num)
	cmd := append([]string{"env", display, "xdotool"}, args...)
	if _, err := m.docker.Exec(ctx, live.session.Container, cmd...); err != nil {
		return fmt.Errorf("桌面输入失败: %w", err)
	}
	return nil
}
