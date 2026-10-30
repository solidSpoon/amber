import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableRow } from '@/components/ui/table';
import { Badge } from '@/components/ui/badge';
import { PageHeader } from '@/components/page-header';
import { StatusBadge, VerdictBadge } from '@/components/session-badges';
import {
  api,
  formatAgo,
  formatBytes,
  machineName,
  type Feedback,
  type Machine,
  type RuntimeInfo,
  type Sample,
  type Session,
} from '../api';

/**
 * 首页看板：每台候选机器的实时屏幕 + 宿主资源 + 最近会话。
 *
 * 「机器」是固定槽位（M1/M2，数量由并发上限决定），没有会话的槽位就是关机态。
 * 这是刻意的拟物：看板上应该一眼看出"几台机器开着、屏幕上在放什么"，
 * 而不是去读一张会话表格。这里不假设被测应用是什么，应用名一律来自会话数据。
 */
export default function Dashboard() {
  const [machines, setMachines] = useState<Machine[]>([]);
  const [samples, setSamples] = useState<Sample[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [info, setInfo] = useState<RuntimeInfo | null>(null);
  const [feedback, setFeedback] = useState<Feedback[]>([]);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    async function load() {
      try {
        const [m, s, list, meta, fb] = await Promise.all([
          api.machines(),
          api.metrics(),
          api.listSessions(),
          api.info(),
          api.feedback(),
        ]);
        if (!alive) return;
        setMachines(m.machines ?? []);
        setSamples(s.samples ?? []);
        setSessions((list.sessions ?? []).slice(0, 5));
        setInfo(meta);
        setFeedback(fb.feedback ?? []);
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
    <div className="flex flex-col gap-8">
      <PageHeader
        title="验收看板"
        description="每台机器是一台一次性测试电脑：起容器、构建应用、全程录屏，结束后销毁。"
      />

      {error && (
        <Card className="border-destructive/30 bg-destructive/5">
          <CardContent className="text-sm text-destructive">读取看板数据失败：{error}</CardContent>
        </Card>
      )}

      <section className="grid gap-6 lg:grid-cols-2">
        {machines.length === 0 && !error && (
          <>
            <MonitorSkeleton />
            <MonitorSkeleton />
          </>
        )}
        {machines.map((m) => (
          <MachineCard key={m.slot} machine={m} display={info?.display} />
        ))}
      </section>

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="宿主 CPU"
          value={latest ? `${latest.cpuPct.toFixed(0)}%` : '—'}
          hint={latest ? `最近采样 ${new Date(latest.at).toLocaleTimeString('zh-CN', { hour12: false })}` : undefined}
        />
        <Stat
          label="内存"
          value={latest ? pct(latest.memUsed, latest.memTotal) : '—'}
          hint={latest ? `${formatBytes(latest.memUsed)} / ${formatBytes(latest.memTotal)}` : undefined}
        />
        <Stat
          label="磁盘"
          value={latest ? pct(latest.diskUsed, latest.diskTotal) : '—'}
          hint={latest ? `${formatBytes(latest.diskUsed)} / ${formatBytes(latest.diskTotal)}` : undefined}
        />
        <Stat
          label="运行中的机器"
          value={latest ? String(latest.containers) : '—'}
          hint={info ? `并发上限 ${info.maxSessions} · 屏幕 ${info.display.width}×${info.display.height}` : undefined}
        />
      </section>

      <Card>
        <CardHeader>
          <CardTitle>最近会话</CardTitle>
          <CardDescription>点会话号进入详情：录像、操作时间线、日志都在那儿。</CardDescription>
          <CardAction>
            <Button variant="outline" size="sm" render={<a href="#/sessions" />}>
              全部会话
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent>
          {sessions.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>还没有会话</EmptyTitle>
                <EmptyDescription>去「验收会话」新建一台，起容器要几分钟。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <Table>
              <TableBody>
                {sessions.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>
                      <a href={`#/sessions/${s.id}`} className="font-mono text-xs hover:underline">
                        {s.id}
                      </a>
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {s.app} <span className="font-mono text-xs">{s.ref || '—'}</span>
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={s.status} />
                    </TableCell>
                    <TableCell>
                      <VerdictBadge verdict={s.verdict} />
                    </TableCell>
                    <TableCell className="text-right text-xs text-muted-foreground">
                      {formatAgo(s.createdAt)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 建议来自 AI 自己：用完这套工作流后觉得哪里别扭，就写在这里。
          它不进验收报告——报告只讲那次验收发生了什么。 */}
      <Card>
        <CardHeader>
          <CardTitle>工作流建议</CardTitle>
          <CardDescription>AI 用完这套流程后留下的意见，用来改产品本身。</CardDescription>
        </CardHeader>
        <CardContent>
          {feedback.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>还没有建议</EmptyTitle>
                <EmptyDescription>AI 觉得哪里别扭时，会通过 feedback 留下意见。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className="flex flex-col divide-y">
              {feedback.map((f) => (
                <div key={f.id} className="flex flex-col gap-1 py-2 first:pt-0 last:pb-0">
                  <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                    <span>{formatAgo(f.at)}</span>
                    {f.rating > 0 && <span>· 评分 {f.rating}/5</span>}
                    {f.sessionId && (
                      <a href={`#/sessions/${f.sessionId}`} className="font-mono hover:underline">
                        · {f.sessionId}
                      </a>
                    )}
                  </div>
                  <p className="text-sm leading-relaxed whitespace-pre-wrap">{f.body}</p>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
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
    <Card>
      <CardHeader>
        <CardDescription>{label}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {hint && <CardContent className="text-xs text-muted-foreground">{hint}</CardContent>}
    </Card>
  );
}

/** 看板首屏的空骨架：比转圈更早给出"这里会出现两台机器"的形状 */
function MonitorSkeleton() {
  return (
    <div className="flex flex-col items-center gap-3">
      <Skeleton className="aspect-video w-full rounded-[16px]" />
      <Skeleton className="h-4 w-40" />
    </div>
  );
}

/**
 * 一台机器：外壳（边框/支架/阴影）+ 屏幕（实时画面或关机态）。
 *
 * 拟物要克制：只做"这块是屏幕"的暗示（内凹、圆角、轻微反光），
 * 不加渐变色块和花哨装饰，否则在浅色界面里会显脏。
 * 屏幕内部颜色是物理属性（屏幕本来就该是黑的），所以不跟着主题走。
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
      <div className="w-full rounded-[26px] bg-muted p-3 shadow-[0_18px_40px_-24px_rgba(0,0,0,0.45)] ring-1 ring-border">
        {/* 屏幕（内凹）；按真实分辨率画比例，1920x1080 就是 16:9，1280x800 就是 16:10 */}
        <div
          className="relative overflow-hidden rounded-[16px] bg-neutral-950 shadow-inner ring-1 ring-black/40"
          style={{ aspectRatio: display ? `${display.width} / ${display.height}` : '16 / 9' }}
        >
          {online && screenSrc && !streamFailed ? (
            <img
              ref={screenRef}
              src={screenSrc}
              alt={`${machineName(machine.slot)} 实时画面`}
              // contain 而不是 cover：分辨率会变，cover 会按框裁掉两侧，应用自己的侧边栏就没了
              className="size-full object-contain"
              onError={() => setStreamFailed(true)}
            />
          ) : (
            <PoweredOff machine={machine} />
          )}

          {/* 屏幕反光：一条很淡的斜向高光，让"这是块玻璃"成立但不抢内容 */}
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-white/10 via-transparent to-transparent" />

          {online && (
            <div className="absolute top-3 left-3 flex items-center gap-2 rounded-full bg-neutral-950/60 px-2.5 py-1 text-[11px] font-medium text-neutral-50 backdrop-blur">
              <span className="relative flex size-1.5">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-75" />
                <span className="relative inline-flex size-1.5 rounded-full bg-success" />
              </span>
              LIVE
            </div>
          )}
        </div>
      </div>

      {/* 支架：颈 + 底座，做出"屏幕立在桌面上"的体积感 */}
      <div className="h-5 w-16 bg-muted shadow-[inset_0_-6px_8px_-6px_rgba(0,0,0,0.35)]" />
      <div className="h-2 w-48 rounded-[3px] bg-muted shadow-[0_6px_16px_-8px_rgba(0,0,0,0.5)]" />

      {/* 铭牌 */}
      <div className="mt-3 flex w-full flex-col gap-1 px-1">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-semibold">{machineName(machine.slot)}</span>
            {online ? (
              <Badge variant="success">{machine.status === 'starting' ? '启动中' : (machine.app ?? '占用中')}</Badge>
            ) : (
              <Badge variant="outline">已关机</Badge>
            )}
          </div>
          {online && machine.sessionId && (
            <a href={`#/sessions/${machine.sessionId}`} className="font-mono text-[11px] hover:underline">
              {machine.sessionId} →
            </a>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
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
  const starting = machine.occupied && machine.status === 'starting';
  return (
    <div className="flex size-full flex-col items-center justify-center gap-3 bg-[radial-gradient(circle_at_50%_30%,rgba(255,255,255,0.06),transparent_60%)] text-neutral-500">
      <div className="flex size-9 items-center justify-center rounded-full border border-neutral-700/70">
        <svg viewBox="0 0 24 24" className="size-4" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M12 3v9" strokeLinecap="round" />
          <path d="M6.5 7a8 8 0 1 0 11 0" strokeLinecap="round" />
        </svg>
      </div>
      <div className="text-[11px] tracking-wide">{starting ? '正在启动…' : '已关机'}</div>
    </div>
  );
}
