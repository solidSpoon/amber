// Package telemetry 采集宿主与容器指标，供管理台的「系统资源」页出曲线。
//
// 采集分两类：
//   - 宿主指标：CPU/内存/磁盘（gopsutil），反映「这台机器还扛得住几个会话」；
//   - 容器计数：按 rigd 标签统计，反映「当前有几个被测机在跑」。
//
// 采样落库而不是只保留内存，是为了刷新页面、重启进程后曲线还能接上。
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/solidSpoon/rigd/internal/dockerx"
	"github.com/solidSpoon/rigd/internal/store"
)

// Sampler 周期性采样并落库。
type Sampler struct {
	store  *store.Store
	docker dockerx.Runner
	log    *slog.Logger
	// dataRoot 用于选择磁盘分区（默认取根分区）。
	dataRoot string
}

// NewSampler 构造采样器。
func NewSampler(st *store.Store, docker dockerx.Runner, dataRoot string, log *slog.Logger) *Sampler {
	return &Sampler{store: st, docker: docker, dataRoot: dataRoot, log: log}
}

// Collect 采一次样。容器计数失败不影响宿主指标的采集——容器可能还没起。
func (s *Sampler) Collect(ctx context.Context) (store.Sample, error) {
	sample := store.Sample{At: time.Now()}

	if percents, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false); err == nil && len(percents) > 0 {
		sample.CPUPct = percents[0]
	} else if err != nil {
		return sample, fmt.Errorf("读取 CPU 使用率失败: %w", err)
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		sample.MemUsed = vm.Used
		sample.MemTotal = vm.Total
	} else {
		return sample, fmt.Errorf("读取内存信息失败: %w", err)
	}

	if du, err := disk.UsageWithContext(ctx, diskRoot(s.dataRoot)); err == nil {
		sample.DiskUsed = du.Used
		sample.DiskTotal = du.Total
	} else {
		return sample, fmt.Errorf("读取磁盘信息失败: %w", err)
	}

	if n, err := s.docker.CountRunning(ctx, "rigd.managed=true"); err == nil {
		sample.Containers = n
	}
	return sample, nil
}

// Run 持续采样，直到 ctx 取消。
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.sampleOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleOnce(ctx)
		}
	}
}

func (s *Sampler) sampleOnce(ctx context.Context) {
	sample, err := s.Collect(ctx)
	if err != nil {
		s.log.Warn("采集指标失败", "err", err)
		return
	}
	if err := s.store.InsertSample(ctx, sample); err != nil {
		s.log.Warn("指标落库失败", "err", err)
	}
}

// diskRoot 取数据目录所在分区；空值退回根分区。
func diskRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
