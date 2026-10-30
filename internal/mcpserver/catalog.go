// MCP 元信息：把「怎么连、能调什么」从服务本身读出来，而不是在页面里手写一份。
//
// 为什么这么做：工具契约一旦在文档里复述一遍，就会和实现漂移。
// 这里在启动时用内存传输连一次自己的 MCP server，走标准的 tools/list 拿真实清单，
// 管理台的「MCP」页展示的就是当前进程实际暴露的东西。
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolInfo 一个对外暴露的工具（用于展示，不含实现）。
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Params 从 inputSchema 里抽出来的必填字段，方便在页面上给出调用示例。
	Required []string `json:"required"`
	// Schema 原始 JSON Schema（前端折叠展示）。
	Schema json.RawMessage `json:"schema,omitempty"`
}

// Catalog 通过内存传输连接 server，取一次 tools/list。
//
// 连接是一次性的：拿完就关，不会占用会话，也不影响后续真实的 stdio/HTTP 客户端。
func Catalog(ctx context.Context, server *mcp.Server) ([]ToolInfo, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	// 服务端必须先连上，客户端才能握手（SDK 的约定）
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("连接内置 MCP 服务失败: %w", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "rigd-introspect", Version: "0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("连接内置 MCP 客户端失败: %w", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("读取工具清单失败: %w", err)
	}

	tools := make([]ToolInfo, 0, len(res.Tools))
	for _, tool := range res.Tools {
		info := ToolInfo{Name: tool.Name, Description: tool.Description}
		if raw, err := json.Marshal(tool.InputSchema); err == nil {
			info.Schema = raw
			info.Required = requiredFields(raw)
		}
		tools = append(tools, info)
	}
	return tools, nil
}

// requiredFields 从 JSON Schema 里读 required 数组；读不出来就返回 nil（页面会只显示描述）。
func requiredFields(schema json.RawMessage) []string {
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil
	}
	return parsed.Required
}
