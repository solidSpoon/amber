// Package dockerx 是容器运行时的抽象层。
//
// 为什么先用 docker CLI 而不是官方 SDK：验收场景只用得到 run/stop/inspect/logs/ps 五个动作，
// CLI 的行为就是用户在终端里看到的行为，排错时不用在两套语义之间翻译。
// 接口单独抽出，后面换成 docker/docker 官方客户端时，只要再实现一个 Runner，
// 上层的调度器与 HTTP/MCP 一行都不用改。
package dockerx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Mount 一个挂载。Source 既可以是宿主路径，也可以是 docker 命名卷名（-v 语法两者同形）。
//
// 用命名卷存依赖缓存是关键：换分支/换会话复用同一份 node_modules 与 yarn 缓存，
// 否则每次起会话都要重新下载 G 级依赖。
type Mount struct {
	Source        string
	ContainerPath string
	ReadOnly      bool
}

// Spec 起一个容器的完整描述。
type Spec struct {
	// Name 容器名，调度器保证唯一（含会话 ID）。
	Name string
	// Image 镜像名。
	Image string
	// Cmd 覆盖镜像的默认命令。
	Cmd []string
	// Env 环境变量（会按 key 排序后注入，保证命令可复现）。
	Env map[string]string
	// Mounts 目录挂载。
	Mounts []Mount
	// Labels 打标签，便于按标签统计/清理。
	Labels map[string]string
	// NetworkHost 使用宿主网络：容器里的 CDP 端口直接落在宿主 loopback 上，
	// Go 侧才能直接连上去驱动应用。
	NetworkHost bool
	// ShmSize /dev/shm 大小，Chromium 系必需（默认 64MB 会随机崩）。
	ShmSize string
}

// State 容器状态摘要。
type State struct {
	Running   bool
	ExitCode  int
	StartedAt time.Time
	FinishedAt time.Time
}

// Runner 容器运行时。
type Runner interface {
	// Run 后台启动容器并返回容器 ID。
	Run(ctx context.Context, spec Spec) (string, error)
	// Stop 停止容器；先 SIGTERM 让容器内录屏正常收尾，超时再强制杀。
	Stop(ctx context.Context, name string, timeout time.Duration) error
	// Inspect 查询容器状态。
	Inspect(ctx context.Context, name string) (State, error)
	// Exec 在容器里执行命令并返回 stdout（用于读取容器内的录像元信息）。
	Exec(ctx context.Context, name string, cmd ...string) (string, error)
	// CountRunning 按标签统计正在运行的容器数（指标页用）。
	CountRunning(ctx context.Context, labelSelector string) (int, error)
	// Logs 取容器日志尾部。
	Logs(ctx context.Context, name string, tail int) (string, error)
	// Remove 删除容器（日志已归档之后再调）。
	Remove(ctx context.Context, name string) error
	// Stats 取容器实时资源占用（看板每台机器下面那行数字）。
	Stats(ctx context.Context, name string) (Stats, error)
}

// Stats 容器资源快照。
type Stats struct {
	CPUPct   float64
	MemUsed  uint64
	MemLimit uint64
}

func (c *CLI) Stats(ctx context.Context, name string) (Stats, error) {
	// --no-stream 取一次；解析 docker 的两段式输出（"1.25%|120MiB / 2GiB"）
	out, err := c.run(ctx, "stats", "--no-stream", "--format", "{{.CPUPerc}}|{{.MemUsage}}", name)
	if err != nil {
		return Stats{}, err
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 2 {
		return Stats{}, fmt.Errorf("docker stats 输出无法解析: %q", out)
	}
	stats := Stats{CPUPct: parsePercent(parts[0])}
	stats.MemUsed, stats.MemLimit = parseMemPair(parts[1])
	return stats, nil
}

// parsePercent "1.25%" → 1.25
func parsePercent(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMemPair "120.5MiB / 2GiB" → (字节, 字节)
func parseMemPair(s string) (uint64, uint64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseBytes(parts[0]), parseBytes(parts[1])
}

// parseBytes 解析 docker 的容量字符串（B/kB/MiB/GiB，二进制单位优先）。
func parseBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		scale  float64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil {
				return 0
			}
			return uint64(v * u.scale)
		}
	}
	return 0
}

// ErrContainerNotFound 表示容器不存在（用于识别「已被外部清掉」）。
var ErrContainerNotFound = errors.New("容器不存在")

// CLI 基于 docker 命令行实现的 Runner。
type CLI struct {
	// Bin docker 可执行文件，默认 "docker"。
	Bin string
}

// NewCLI 构造一个 CLI Runner。
func NewCLI(bin string) *CLI {
	if bin == "" {
		bin = "docker"
	}
	return &CLI{Bin: bin}
}

// Run 用 `docker run -d` 起容器。
//
// 刻意不用 --rm：容器退出后我们还要收集它的 stdout/stderr（应用为什么没起来就在里面），
// 取完日志再 Remove。
func (c *CLI) Run(ctx context.Context, spec Spec) (string, error) {
	if spec.Name == "" || spec.Image == "" {
		return "", errors.New("容器名与镜像不能为空")
	}

	// 不加 --rm：容器退出后还要 docker logs 取日志、darwin 归档，取完由 Remove 清理
	args := []string{"run", "-d", "--name", spec.Name}
	if spec.NetworkHost {
		args = append(args, "--network", "host")
	}
	if spec.ShmSize != "" {
		args = append(args, "--shm-size", spec.ShmSize)
	}
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	for _, m := range spec.Mounts {
		mount := m.Source + ":" + m.ContainerPath
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	args = append(args, spec.Image)
	args = append(args, spec.Cmd...)

	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Stop 停容器。SIGTERM 让容器内的收尾流程（停录屏、写 session.json）跑完。
func (c *CLI) Stop(ctx context.Context, name string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	if seconds <= 0 {
		seconds = 20
	}
	if _, err := c.run(ctx, "stop", "--time", strconv.Itoa(seconds), name); err != nil {
		if strings.Contains(err.Error(), "No such container") {
			return ErrContainerNotFound
		}
		return err
	}
	return nil
}

// Inspect 读容器状态。
func (c *CLI) Inspect(ctx context.Context, name string) (State, error) {
	format := `{{.State.Running}}|{{.State.ExitCode}}|{{.State.StartedAt}}|{{.State.FinishedAt}}`
	out, err := c.run(ctx, "inspect", "-f", format, name)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") || strings.Contains(err.Error(), "No such container") {
			return State{}, ErrContainerNotFound
		}
		return State{}, err
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 4 {
		return State{}, fmt.Errorf("docker inspect 输出无法解析: %q", out)
	}
	state := State{
		Running:  strings.TrimSpace(parts[0]) == "true",
		ExitCode: atoi(strings.TrimSpace(parts[1])),
	}
	state.StartedAt = parseDockerTime(parts[2])
	state.FinishedAt = parseDockerTime(parts[3])
	return state, nil
}

// Exec 在容器里跑一条命令，返回 stdout。
func (c *CLI) Exec(ctx context.Context, name string, cmd ...string) (string, error) {
	args := append([]string{"exec", name}, cmd...)
	return c.run(ctx, args...)
}

// CountRunning 按标签统计运行中的容器。
func (c *CLI) CountRunning(ctx context.Context, labelSelector string) (int, error) {
	args := []string{"ps", "-q"}
	if labelSelector != "" {
		args = append(args, "--filter", "label="+labelSelector)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0, nil
	}
	return len(lines), nil
}

// Logs 取容器日志尾部。
func (c *CLI) Logs(ctx context.Context, name string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	out, err := c.run(ctx, "logs", "--tail", strconv.Itoa(tail), name)
	if err != nil {
		return out, err
	}
	return out, nil
}

// Remove 删除容器；容器不存在视作成功（幂等）。
func (c *CLI) Remove(ctx context.Context, name string) error {
	if _, err := c.run(ctx, "rm", "-f", name); err != nil {
		if strings.Contains(err.Error(), "No such container") {
			return nil
		}
		return err
	}
	return nil
}

// run 执行 docker 命令，把 stderr 带进错误信息（排错时不至于只剩 exit code）。
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("docker %s 失败: %s", strings.Join(args[:min(2, len(args))], " "), msg)
	}
	return out, nil
}

// Stream 启动一条长时间运行的容器命令（预留：流式日志）。
func (c *CLI) Stream(ctx context.Context, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return pipe, nil
}

// LabelSelector 生成会话容器的统一标签，便于统计与清理。
func LabelSelector(sessionID string) string {
	return "rigd.session=" + sessionID
}

// DefaultLabels 会话容器的标准标签集合。
func DefaultLabels(sessionID string) map[string]string {
	return map[string]string{
		"rigd.managed": "true",
		"rigd.session": sessionID,
	}
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 稳定顺序，保证 docker run 命令行可复现（日志里比对更省事）
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseDockerTime 解析 docker 的 RFC3339Nano 时间（空值表示未发生）。
func parseDockerTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0001-01-01") {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// MarshalJSON 让 Spec 能被直接 dump 进日志（不含敏感信息，目前没有敏感字段）。
func (s Spec) MarshalJSON() ([]byte, error) {
	type alias Spec
	return json.Marshal(alias(s))
}
