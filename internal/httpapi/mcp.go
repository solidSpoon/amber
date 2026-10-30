// MCP 接入：把「外部 AI 怎么连上这台机器」暴露成接口 + 页面数据。
//
// 两种接入方式，各自解决不同场景：
//   - stdio（rigd --mcp）：同机使用，最简单，不需要网络与令牌；
//   - Streamable HTTP（/mcp）：跨机器使用（比如在 Mac 上跑 AI 连这台 Linux），必须带令牌。
package httpapi

import (
	"net"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpInfo 给管理台的「MCP」页：地址、令牌、启动命令、工具清单。
func (s *Server) mcpInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"httpEnabled": s.mcp.HTTPEnabled,
		"httpUrl":     s.mcp.HTTPURL,
		"token":       s.mcp.Token,
		"stdio": map[string]any{
			"command": s.mcp.Command,
			"env":     s.mcp.Env,
		},
		"tools": s.mcp.Tools,
	})
}

// mcpGuard 保护 HTTP 接入点：本机直连放行，其它来源必须带正确令牌。
//
// 为什么不一律要令牌：同机 AI 客户端（stdio 或 127.0.0.1）本来就有完全的本机权限，
// 再拦一层只会让人去关掉它；真正要防的是「局域网里谁能起容器」。
func mcpGuard(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" || got != token {
			writeError(w, http.StatusUnauthorized, errUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type mcpUnauthorized struct{}

func (mcpUnauthorized) Error() string {
	return "需要访问令牌：Authorization: Bearer <RIGD_MCP_TOKEN>"
}

var errUnauthorized = mcpUnauthorized{}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewMCPHandler 生成挂到 /mcp 的处理器（Streamable HTTP）。
//
// 每次请求都从同一个 server 实例取，保证工具集合唯一、状态一致。
func NewMCPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}

// Guard 暴露给 main 组合使用。
func Guard(token string, next http.Handler) http.Handler { return mcpGuard(token, next) }
