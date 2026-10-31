// MCP 接入：把「外部 AI 怎么连上这台机器」暴露成接口 + 页面数据。
//
// 两种接入方式，各自解决不同场景：
//   - stdio（rigd --mcp）：同机使用；
//   - Streamable HTTP（/mcp）：跨机器使用（比如在 Mac 上跑 AI 连这台 Linux）。
//
// 这里不做鉴权：它本来就是内网里给自己人用的验收机接口，
// 真正的边界在网络层（只在内网监听、不放到公网）。
package httpapi

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpInfo 给管理台的「MCP」页：地址、令牌、启动命令、工具清单。
func (s *Server) mcpInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"httpEnabled": s.mcp.HTTPEnabled,
		"httpUrl":     s.mcp.HTTPURL,
		"stdio": map[string]any{
			"command": s.mcp.Command,
			"env":     s.mcp.Env,
		},
		"tools": s.mcp.Tools,
	})
}

// NewMCPHandler 生成挂到 /mcp 的处理器（Streamable HTTP）。
//
// 每次请求都从同一个 server 实例取，保证工具集合唯一、状态一致。
func NewMCPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
