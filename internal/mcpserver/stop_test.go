package mcpserver

// stop_test.go：收尾工具只收一个会话 ID。
//
// 这里曾经还有 verdict（pass/fail/inconclusive）和 summary —— 那是把 Amber 当
// 「AI 验收平台」时留下的盖章参数。它现在只记录发生了什么，判断留给读记录的人，
// 所以参数表里不该再有结论。
//
// 这条测试盯的是「别再把这个概念加回来」。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
)

func TestStopToolTakesOnlySessionID(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := &config.Config{DataDir: t.TempDir(), Image: "x", MaxSessions: 1, CDPPortBase: 39222, VNCPortBase: 36080}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tools, err := Catalog(ctx, NewServer(session.NewManager(cfg, st, noopDocker{}, nil, log), st, cfg, log))
	if err != nil {
		t.Fatalf("读取工具清单失败: %v", err)
	}

	var schema json.RawMessage
	for _, tool := range tools {
		if tool.Name == "amber_stop" {
			schema = tool.Schema
		}
	}
	if schema == nil {
		t.Fatal("工具清单里没有 amber_stop")
	}

	var parsed struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("解析 schema 失败: %v", err)
	}
	if len(parsed.Properties) != 1 {
		t.Fatalf("收尾工具的参数 = %v，期望只有 sessionId", parsed.Properties)
	}
	if _, ok := parsed.Properties["sessionId"]; !ok {
		t.Fatal("缺少 sessionId")
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "sessionId" {
		t.Fatalf("必填字段 = %v，期望只有 sessionId", parsed.Required)
	}
	for _, gone := range []string{"verdict", "summary"} {
		if _, ok := parsed.Properties[gone]; ok {
			t.Fatalf("收尾工具不该再有 %q：判断由读记录的人下", gone)
		}
	}
}
