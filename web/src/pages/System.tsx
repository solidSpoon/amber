import { useEffect, useState } from 'react';
import { api, formatBytes, subscribe, type Sample } from '../api';

/** 系统资源：宿主 CPU/内存/磁盘 + 当前跑着几个被测容器。 */
export default function System() {
  const [samples, setSamples] = useState<Sample[]>([]);

  useEffect(() => {
    void api.metrics().then((data) => setSamples(data.samples ?? []));
    // 实时更新用 SSE，避免自己写轮询与去重
    return subscribe<{ samples: Sample[] }>('/api/system/stream', (payload) => {
      setSamples(payload.samples ?? []);
    });
  }, []);

  const latest = samples.at(-1);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold">系统资源</h1>
        <p className="mt-1 text-sm text-neutral-500">
          决定「还能并行几台测试机」的就是这几个数：CPU、内存、磁盘，以及当前容器数。
        </p>
      </header>

      <div className="grid gap-4 md:grid-cols-4">
        <Metric label="CPU" value={latest ? `${latest.cpuPct.toFixed(0)}%` : '—'} data={samples.map((s) => s.cpuPct)} />
        <Metric
          label="内存"
          value={latest ? `${pct(latest.memUsed, latest.memTotal)}%` : '—'}
          hint={latest ? `${formatBytes(latest.memUsed)} / ${formatBytes(latest.memTotal)}` : ''}
          data={samples.map((s) => (s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0))}
        />
        <Metric
          label="磁盘"
          value={latest ? `${pct(latest.diskUsed, latest.diskTotal)}%` : '—'}
          hint={latest ? `${formatBytes(latest.diskUsed)} / ${formatBytes(latest.diskTotal)}` : ''}
          data={samples.map((s) => (s.diskTotal ? (s.diskUsed / s.diskTotal) * 100 : 0))}
        />
        <Metric label="运行中的容器" value={latest ? String(latest.containers) : '—'} data={samples.map((s) => s.containers)} />
      </div>

      <section className="rounded-lg border border-neutral-800 bg-neutral-900/40 p-4">
        <h2 className="mb-2 text-sm font-medium text-neutral-400">采样</h2>
        <p className="text-xs text-neutral-500">
          共 {samples.length} 条，最近一条 {latest ? new Date(latest.at).toLocaleTimeString() : '—'}。
          采样间隔由 RIGD_SAMPLE_INTERVAL 控制（默认 5s）。
        </p>
      </section>
    </div>
  );
}

function pct(used: number, total: number): string {
  if (!total) return '0';
  return ((used / total) * 100).toFixed(0);
}

/** 指标卡：数值 + 一条极简折线（内联 SVG，不引图表库）。 */
function Metric({ label, value, hint, data }: { label: string; value: string; hint?: string; data: number[] }) {
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-900/40 p-4">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-neutral-500">{hint}</div>}
      <Sparkline data={data} />
    </div>
  );
}

function Sparkline({ data }: { data: number[] }) {
  const points = data.slice(-60);
  if (points.length < 2) return <div className="mt-3 h-10" />;
  const max = Math.max(...points, 1);
  const min = Math.min(...points, 0);
  const span = max - min || 1;
  const path = points
    .map((v, i) => {
      const x = (i / (points.length - 1)) * 100;
      const y = 100 - ((v - min) / span) * 100;
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(' ');
  return (
    <svg viewBox="0 0 100 100" preserveAspectRatio="none" className="mt-3 h-10 w-full">
      <path d={path} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" className="text-emerald-400" />
    </svg>
  );
}
