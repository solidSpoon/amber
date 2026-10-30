// Package config 从环境变量装载进程配置。
//
// 约定：这里只做「读 + 校验」，不做任何副作用（除了创建数据目录）。
// 缺关键项就返回错误让进程启动失败——后台服务静默降级比崩掉更难查。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是 rigd 的全部可调参数。
type Config struct {
	// Addr HTTP 监听地址，默认只绑 127.0.0.1。
	Addr string
	// DataDir 数据根目录：sqlite、每个会话的录像与截图都落在这里。
	DataDir string
	// Image 起会话用的容器镜像。
	Image string
	// ModelsDir 预置模型目录（宿主机），只读挂到容器 /assets/models。
	// 需要离线推理的应用（如 DashPlayer 的本地翻译）靠它避免会话内下载。
	ModelsDir string
	// MediaDir 验收素材目录（宿主机），只读挂到容器 /assets。
	// 需要媒体素材的应用（如播放器）必须配置它，否则起会话时直接报错。
	MediaDir string
	// DockerBin docker 可执行文件路径，便于测试替换。
	DockerBin string
	// MaxSessions 同时允许运行的会话数；超出直接拒绝，而不是排队等待。
	MaxSessions int
	// IdleTimeout 会话空闲多久被回收（无工具调用/无 HTTP 轮询）。
	IdleTimeout time.Duration
	// MaxLifetime 会话最长存活时间，防止客户端忘关。
	MaxLifetime time.Duration
	// CDPPortBase / VNCPortBase 端口分配起点，会话按需递增。
	CDPPortBase int
	VNCPortBase int
	// Display 虚拟屏尺寸。
	Display DisplayConfig
	// Record 录屏参数。
	Record RecordConfig
	// SampleInterval 指标采样间隔。
	SampleInterval time.Duration
	// LogLevel 日志级别。
	LogLevel slog.Level
	// MCPToken HTTP 方式接 MCP 的访问令牌。为空时启动会生成一个并落盘，
	// 避免把「能起容器的接口」裸奔在局域网上。
	MCPToken string
}

// DisplayConfig 虚拟屏尺寸与窗口环境。
type DisplayConfig struct {
	Width  int
	Height int
	// Num X 显示号（:Num）。控制面在容器里执行窗口操作时要显式带上它：
	// docker exec 拿不到 entrypoint 里 export 的环境变量。
	Num int
}

// RecordConfig 录屏参数。
type RecordConfig struct {
	FPS int
	// CRF 越大体积越小、画质越差；26 在 1280x800 的桌面录屏上够看清字幕。
	CRF int
}

// Load 读取环境变量并校验。任何不合法都返回错误，由 main 直接退出。
func Load() (*Config, error) {
	cfg := &Config{
		Addr:           env("RIGD_ADDR", "127.0.0.1:8088"),
		DataDir:        env("RIGD_DATA_DIR", defaultDataDir()),
		Image:          env("RIGD_IMAGE", "electron-acceptance-rig:latest"),
		DockerBin:      env("RIGD_DOCKER", "docker"),
		MediaDir:       env("RIGD_MEDIA_DIR", ""),
		ModelsDir:      env("RIGD_MODELS_DIR", ""),
		MaxSessions:    envInt("RIGD_MAX_SESSIONS", 2),
		IdleTimeout:    envDuration("RIGD_IDLE_TIMEOUT", 10*time.Minute),
		MaxLifetime:    envDuration("RIGD_MAX_LIFETIME", 40*time.Minute),
		CDPPortBase:    envInt("RIGD_CDP_PORT_BASE", 9222),
		VNCPortBase:    envInt("RIGD_VNC_PORT_BASE", 6080),
		SampleInterval: envDuration("RIGD_SAMPLE_INTERVAL", 5*time.Second),
		MCPToken:       env("RIGD_MCP_TOKEN", ""),
		LogLevel:       envLogLevel("RIGD_LOG_LEVEL", slog.LevelInfo),
		Display: DisplayConfig{
			Width:  envInt("RIGD_DISPLAY_W", 1280),
			Height: envInt("RIGD_DISPLAY_H", 800),
			Num:    envInt("RIGD_DISPLAY_NUM", 99),
		},
		Record: RecordConfig{
			FPS: envInt("RIGD_RECORD_FPS", 20),
			CRF: envInt("RIGD_RECORD_CRF", 26),
		},
	}

	if cfg.MaxSessions < 1 {
		return nil, fmt.Errorf("RIGD_MAX_SESSIONS 必须 >= 1，收到 %d", cfg.MaxSessions)
	}
	if cfg.Display.Width < 640 || cfg.Display.Height < 480 {
		return nil, fmt.Errorf("虚拟屏尺寸过小：%dx%d", cfg.Display.Width, cfg.Display.Height)
	}
	if cfg.Display.Num < 0 || cfg.Display.Num > 99 {
		return nil, fmt.Errorf("RIGD_DISPLAY_NUM 需在 0~99，收到 %d", cfg.Display.Num)
	}
	if cfg.Record.FPS < 1 || cfg.Record.FPS > 60 {
		return nil, fmt.Errorf("RIGD_RECORD_FPS 需在 1~60，收到 %d", cfg.Record.FPS)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s 失败: %w", cfg.DataDir, err)
	}
	return cfg, nil
}

// LoadOrCreateMCPToken 读取（或首次生成）MCP 访问令牌。
//
// 令牌存在数据目录里而不是每次都重新生成：否则重启后所有客户端配置都得改一遍。
func (c *Config) LoadOrCreateMCPToken() (string, error) {
	if c.MCPToken != "" {
		return c.MCPToken, nil
	}
	path := filepath.Join(c.DataDir, "mcp.token")
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			c.MCPToken = token
			return token, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 MCP 令牌失败: %w", err)
	}
	token := "rigd_" + hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写入 MCP 令牌失败: %w", err)
	}
	c.MCPToken = token
	return token, nil
}

// SessionsDir 会话产物根目录（每个会话一个子目录）。
func (c *Config) SessionsDir() string { return filepath.Join(c.DataDir, "sessions") }

// DBPath sqlite 文件路径。
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "rigd.db") }

func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "rigd")
	}
	return "./rigd-data"
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envLogLevel(key string, fallback slog.Level) slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	}
	return fallback
}
