import { useEffect, useRef, useState } from 'react';
import {
  api,
  formatAgo,
  formatBytes,
  machineName,
  type Machine,
  type RuntimeInfo,
  type Sample,
  type Session,
} from '../api';

/**
 * 首页看板：两台候选机器的实时屏幕 + 资源概览 + 最近会话。
 *
 * 「机器」是固定槽位（M1/M2），不作为的会话就是关机状态——这是刻意的拟物：
 * 用户在看板上应该一眼看出"现在有几台机器开着、屏幕上在放什么"，
 * 而不是去读一张会话表格。
 */
export default function Dashboard() {
  const [machines, setMachines] = useState<Machine[]>([]);
  const [samples, setSamples] = useState<Sample[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [info, setInfo] = useState<RuntimeInfo | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    async function load() {
      try {
        const [m, s, list, meta] = await Promise.all([
          api.machines(),
          api.metrics(),
          api.listSessions(),
          api.info(),
        ]);
        if (!alive) return;
        setMachines(m.machines ?? []);
        setSamples(s.samples ?? []);
        setSessions((list.sessions ?? []).slice(0, 5));
        setInfo(meta);
        setError('');
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      }
    }
    void load();
    // 看板用轮询：数值是"看一眼"的语义，2 秒一次足够，也避免长连接带来的各种边界情况
    const timer = window.setInterval(load, 2000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, []);

  const latest = samples.at(-1);

  return (
    <div className="space-y-8">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-neutral-900">验收看板</h1>
          <p className="mt-1 text-sm text-neutral-500">
            每台机器是一台一次性测试电脑：起容器、构建应用、全程录屏，结束后销毁。
          </p>
        </div>
        {error && <span className="text-sm text-red-600">{error}</span>}
      </header>

      <section className="grid gap-8 lg:grid-cols-2">
        {machines.length === 0 && (
          <div className="col-span-full rounded-xl border border-dashed border-neutral-300 bg-white/60 p-10 text-center text-sm text-neutral-400">
            正在读取机器状态…
          </div>
        )}
        {machines.map((m) => (
          <MachineCard key={m.slot} machine={m} display={info?.display} />
        ))}
      </section>

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="宿主 CPU" value={latest ? `${latest.cpuPct.toFixed(0)}%` : '—'} />
        <Stat
          label="内存"
          value={latest ? pct(latest.memUsed, latest.memTotal) : '—'}
          hint={latest ? `${formatBytes(latest.memUsed)} / ${formatBytes(latest.memTotal)}` : ''}
        />
        <Stat
          label="磁盘"
          value={latest ? pct(latest.diskUsed, latest.diskTotal) : '—'}
          hint={latest ? `${formatBytes(latest.diskUsed)} / ${formatBytes(latest.diskTotal)}` : ''}
        />
        <Stat label="运行中的机器" value={latest ? `${latest.containers}` : '—'} hint="按 rigd.managed 标签统计" />
      </section>

      <section>
        <div className="mb-3 flex items-baseline justify-between">
          <h2 className="text-sm font-medium text-neutral-600">最近会话</h2>
          <a href="#/sessions" className="text-sm text-sky-600 hover:underline">
            全部会话 →
          </a>
        </div>
        <div className="overflow-hidden rounded-xl border border-neutral-200 bg-white">
          <table className="w-full text-sm">
            <tbody>
              {sessions.length === 0 && (
                <tr>
                  <td className="px-4 py-8 text-center text-neutral-400">还没有会话</td>
                </tr>
              )}
              {sessions.map((s) => (
                <tr key={s.id} className="border-t border-neutral-100 first:border-t-0 hover:bg-neutral-50">
                  <td className="px-4 py-2">
                    <a href={`#/sessions/${s.id}`} className="font-mono text-xs text-sky-600 hover:underline">
                      {s.id}
                    </a>
                  </td>
                  <td className="px-4 py-2 text-neutral-600">
                    {s.app} <span className="font-mono text-xs text-neutral-400">{s.ref}</span>
                  </td>
                  <td className="px-4 py-2 text-neutral-500">{s.status}</td>
                  <td className={`px-4 py-2 ${verdictColor(s.verdict)}`}>{s.verdict || '—'}</td>
                  <td className="px-4 py-2 text-right text-neutral-400">{formatAgo(s.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function verdictColor(verdict?: string): string {
  if (verdict === 'pass') return 'text-emerald-600';
  if (verdict === 'fail') return 'text-red-600';
  if (verdict === 'inconclusive') return 'text-amber-600';
  return 'text-neutral-400';
}

/** 已运行时长：看板读的是"开了多久"，不是"几分钟前" */
function formatUptime(since: string): string {
  const ms = Date.now() - new Date(since).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return '不到 1 分钟';
  if (mins < 60) return `${mins} 分钟`;
  const hours = Math.floor(mins / 60);
  return `${hours} 小时 ${mins % 60} 分`;
}

function pct(used: number, total: number): string {
  if (!total) return '0%';
  return `${((used / total) * 100).toFixed(0)}%`;
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border border-neutral-200 bg-white px-4 py-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums text-neutral-900">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-neutral-400">{hint}</div>}
    </div>
  );
}

/**
 * 一台机器：外壳（边框/支架/阴影）+ 屏幕（实时画面或关机态）。
 *
 * 拟物要克制：只做"这块是屏幕"的暗示（内凹、圆角、轻微内阴影、屏幕反光），
 * 不加渐变色块和花哨装饰，否则在浅色界面里会显脏。
 */
function MachineCard({ machine, display }: { machine: Machine; display?: { width: number; height: number } }) {
  const online = machine.occupied && (machine.status === 'ready' || machine.status === 'starting');
  const screenRef = useRef<HTMLImageElement>(null);
  const [streamFailed, setStreamFailed] = useState(false);

  // 每次切换会话都重建 <img src>，避免沿用上一个会话的帧
  useEffect(() => {
    setStreamFailed(false);
  }, [machine.sessionId]);

  const screenSrc = machine.sessionId ? `/api/sessions/${machine.sessionId}/screen.mjpg` : '';

  return (
    <article className="mx-auto flex w-full max-w-[880px] flex-col items-center">
      {/* 机身 */}
      <div className="w-full rounded-[26px] bg-gradient-to-b from-neutral-200 to-neutral-300 p-3 shadow-[0_18px_40px_-24px_rgba(0,0,0,0.45)] ring-1 ring-black/5">
        {/* 屏幕（内凹） */}
        {/* 屏幕按真实分辨率画：1920x1080 就是 16:9，1280x800 就是 16:10，不写死 */}
        <div
          className="relative overflow-hidden rounded-[16px] bg-neutral-950 shadow-inner ring-1 ring-black/40"
          style={{ aspectRatio: display ? `${display.width} / ${display.height}` : '16 / 9' }}
        >
          {online && screenSrc && !streamFailed ? (
            <img
              ref={screenRef}
              src={screenSrc}
              alt={`${machineName(machine.slot)} 实时画面`}
              // contain 而不是 cover：屏幕尺寸随分辨率变（1280x800 / 1920x1080），
              // cover 会按框裁掉两侧，应用自己的侧边栏就被切没了
              className="h-full w-full object-contain"
              onError={() => setStreamFailed(true)}
            />
          ) : (
            <PoweredOff machine={machine} />
          )}

          {/* 屏幕反光：一条很淡的斜向高光，让"这是块玻璃"成立但不抢内容 */}
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-white/10 via-transparent to-transparent" />

          {online && (
            <div className="absolute left-3 top-3 flex items-center gap-2 rounded-full bg-black/55 px-2.5 py-1 text-[11px] font-medium text-white backdrop-blur">
              <span className="relative flex h-1.5 w-1.5">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-emerald-400" />
              </span>
              LIVE
            </div>
          )}
        </div>
      </div>

      {/* 支架：颈 + 底座，做出"屏幕立在桌面上"的体积感 */}
      <div className="h-5 w-16 bg-gradient-to-b from-neutral-300 via-neutral-200 to-neutral-300 shadow-[inset_0_-6px_8px_-6px_rgba(0,0,0,0.35)]" />
      <div className="h-2 w-48 rounded-[3px] bg-gradient-to-b from-neutral-200 to-neutral-300 shadow-[0_6px_16px_-8px_rgba(0,0,0,0.5)]" />

      {/* 铭牌 */}
      <div className="mt-3 w-full px-1">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-semibold text-neutral-900">{machineName(machine.slot)}</span>
            {online ? (
              <span className="rounded-md border border-emerald-200 bg-emerald-50 px-1.5 py-0.5 text-[11px] text-emerald-700">
                {machine.status === 'starting' ? '启动中' : machine.app}
              </span>
            ) : (
              <span className="rounded-md border border-neutral-200 bg-neutral-50 px-1.5 py-0.5 text-[11px] text-neutral-500">
                已关机
              </span>
            )}
          </div>
          {online && machine.sessionId && (
            <a
              href={`#/sessions/${machine.sessionId}`}
              className="font-mono text-[11px] text-sky-600 hover:underline"
            >
              {machine.sessionId} →
            </a>
          )}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-neutral-400">
          {online ? (
            <>
              <span className="font-mono">{machine.ref || '—'}</span>
              {machine.statsOk ? (
                <>
                  <span>CPU {machine.stats.CPUPct.toFixed(0)}%</span>
                  <span>内存 {formatBytes(machine.stats.MemUsed)}</span>
                </>
              ) : (
                <span>统计中…</span>
              )}
              {machine.since && <span>已运行 {formatUptime(machine.since)}</span>}
            </>
          ) : (
            <span>空闲槽位，随时可以接单</span>
          )}
        </div>
      </div>
    </article>
  );
}

/** 关机态：屏幕是暗的，只留一点点光与状态字，暗示"这块屏还在，只是没开" */
function PoweredOff({ machine }: { machine: Machine }) {
  const idle = machine.occupied && machine.status === 'starting';
  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-3 bg-[radial-gradient(circle_at_50%_30%,rgba(255,255,255,0.06),transparent_60%)] text-neutral-500">
      <div className="flex h-9 w-9 items-center justify-center rounded-full border border-neutral-700/70">
        <svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M12 3v9" strokeLinecap="round" />
          <path d="M6.5 7a8 8 0 1 0 11 0" strokeLinecap="round" />
        </svg>
      </div>
      <div className="text-[11px] tracking-wide">{idle ? '正在启动…' : '已关机'}</div>
    </div>
  );
}
