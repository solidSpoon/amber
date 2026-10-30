package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

type noopDocker struct{}

func (noopDocker) Run(context.Context, dockerx.Spec) (string, error) { return "cid", nil }
func (noopDocker) Stop(context.Context, string, time.Duration) error { return nil }
func (noopDocker) Inspect(context.Context, string) (dockerx.State, error) {
	return dockerx.State{}, nil
}
func (noopDocker) Exec(context.Context, string, ...string) (string, error) { return "", nil }
func (noopDocker) CountRunning(context.Context, string) (int, error)       { return 0, nil }
func (noopDocker) Logs(context.Context, string, int) (string, error)       { return "", nil }
func (noopDocker) Remove(context.Context, string) error                    { return nil }
func (noopDocker) Stats(context.Context, string) (dockerx.Stats, error)    { return dockerx.Stats{}, nil }

// 工具清单是管理台「MCP」页的数据源，必须和真正注册的工具一致。
// 这条测试直接连服务读 tools/list，防止以后加工具忘了同步页面文档。
func TestCatalogMatchesRegisteredTools(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{DataDir: t.TempDir(), Image: "x", MaxSessions: 1, CDPPortBase: 39222, VNCPortBase: 36080}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(session.NewManager(cfg, st, noopDocker{}, nil, log), st, log)

	tools, err := Catalog(ctx, server)
	if err != nil {
		t.Fatalf("读取工具清单失败: %v", err)
	}

	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Name)
		if strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("工具 %s 缺少描述（模型只能靠描述判断何时调用）", tool.Name)
		}
	}

	// 工具面刻意只有这四个：会话的开、跑、收 + 对工作流本身的反馈。
	// 具体动作（点击、读结构、等待…）都走 batch 的 steps，不再各占一个工具。
	want := []string{
		"batch", "end_session", "feedback", "list_sessions", "start_session",
	}
	if len(got) != len(want) {
		t.Fatalf("工具数量 = %d，期望 %d：%v", len(got), len(want), got)
	}
	// SDK 返回顺序按名字排序，这里按集合比较，避免依赖排序细节
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("工具集合不一致：%v（期望 %v）", got, want)
		}
	}

	// 会话类工具必须标出必填参数，页面上的调用示例依赖它
	for _, tool := range tools {
		if tool.Name == "start_session" && len(tool.Required) == 0 {
			t.Fatal("start_session 应至少有一个必填参数（app）")
		}
	}
}
