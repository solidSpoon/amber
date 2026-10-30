// Command rigd 是验收流水线的控制面进程。
//
// 两种运行模式：
//
//	rigd            → HTTP 管理台 + 会话调度（默认）
//	rigd --mcp      → MCP 服务（stdio），给外部 AI 客户端驱动
//
// 两种模式共用同一套 store/session，所以「AI 通过 MCP 起的会话」在管理台里也能看到，
// 反之亦然——这正是把控制面和驱动方解耦的意义。
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/solidSpoon/rigd/internal/config"
	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/httpapi"
	"github.com/solidSpoon/rigd/internal/mcpserver"
	"github.com/solidSpoon/rigd/internal/session"
	"github.com/solidSpoon/rigd/internal/store"
	"github.com/solidSpoon/rigd/internal/telemetry"
	"github.com/solidSpoon/rigd/web"
)

func main() {
	mode := flag.String("mcp", "", "以 MCP stdio 模式运行：可选值为空(FALSE)或 true")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("配置无效", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		logger.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	docker := dockerx.NewCLI(cfg.DockerBin)
	manager := session.NewManager(cfg, st, docker, nil, logger)

	if *mode == "true" {
		runMCP(ctx, manager, logger)
		return
	}
	runHTTP(ctx, cfg, st, manager, docker, logger)
}

// runMCP 把工具面挂到 stdio 上。注意：MCP 模式下不启动 HTTP，管理台与 MCP 是两个进程，
// 想同时用就开两个——它们共享同一个 sqlite，会话状态不会打架。
func runMCP(ctx context.Context, manager *session.Manager, logger *slog.Logger) {
	server := mcpserver.NewServer(manager, logger)
	logger.Info("MCP 服务已启动（stdio）")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("MCP 服务退出", "err", err)
		os.Exit(1)
	}
}

// runHTTP 启动管理台：调度器扫超时、采样器采指标、HTTP 出接口与页面。
func runHTTP(ctx context.Context, cfg *config.Config, st *store.Store, manager *session.Manager, docker dockerx.Runner, logger *slog.Logger) {
	if n, err := manager.RecoverSessions(ctx); err != nil {
		logger.Warn("接管历史会话失败", "err", err)
	} else if n > 0 {
		logger.Info("接管历史会话完成", "count", n)
	}
	go manager.RunSweeper(ctx, 30*time.Second)
	go telemetry.NewSampler(st, docker, cfg.DataDir, logger).Run(ctx, cfg.SampleInterval)

	webFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		logger.Warn("前端资源不可用，只提供 API", "err", err)
		webFS = nil
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg, st, manager, webFS, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("管理台已启动", "addr", cfg.Addr, "data", cfg.DataDir)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("收到退出信号，正在收尾")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)

	// 退出前把仍在跑的会话正常收尾，别留下没人管的容器
	for _, sess := range mustActive(st, logger) {
		if _, err := manager.Stop(shutdownCtx, sess, "", "服务关闭时自动收尾"); err != nil {
			logger.Warn("收尾会话失败", "session", sess, "err", err)
		}
	}
}

func mustActive(st *store.Store, logger *slog.Logger) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sessions, err := st.ActiveSessions(ctx)
	if err != nil {
		logger.Warn("读取活跃会话失败", "err", err)
		return nil
	}
	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		ids = append(ids, sess.ID)
	}
	return ids
}
