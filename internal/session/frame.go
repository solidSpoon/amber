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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// probeWindowGeometry 现量一次窗口几何（只读，不改窗口）。
//
// 不去读 window.json 那类"某一刻的快照"：应用随时可能自己改窗口，
// 想知道"现在多大"，只能现场量。
func (m *Manager) probeWindowGeometry(ctx context.Context, container string, slot int) (store.Rect, store.Rect, error) {
	display := fmt.Sprintf("DISPLAY=:%d", m.displayOf(slot))
	out, err := m.docker.Exec(ctx, container, "env", display, "rig-window-geometry")
	if err != nil {
		return store.Rect{}, store.Rect{}, err
	}
	var meta windowMeta
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		return store.Rect{}, store.Rect{}, fmt.Errorf("解析窗口几何失败: %w", err)
	}
	displayRect := store.Rect{Width: meta.Display.Width, Height: meta.Display.Height}
	frame := store.Rect{
		X: meta.Window.X, Y: meta.Window.Y,
		Width: meta.Window.Width, Height: meta.Window.Height,
	}
	if !displayRect.Known() || !frame.Known() {
		return store.Rect{}, store.Rect{}, errors.New("量不到窗口几何")
	}
	return frame, displayRect, nil
}
