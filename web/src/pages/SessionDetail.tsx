import { useEffect, useRef, useState } from 'react';
import { ArrowLeftIcon, ExternalLinkIcon, CheckIcon, XIcon, PauseIcon, PlayIcon, Maximize2Icon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Skeleton } from '@/components/ui/skeleton';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { ScreenStage } from '@/components/screen-stage';
import { StatusBadge, VerdictBadge } from '@/components/session-badges';
import { api, formatAgo, type LogSource, type Session, type SessionEvent } from '../api';

/**
 * 会话详情 = 验收报告。
 *
 * 排版按「一篇文章」来：左边是这一步做了什么、为什么、结果，右边是这一步的画面的小循环播放，
 * 点一下才放大。刻意不用卡片框——一屏有十几个步骤时，边框会把阅读节奏切碎。
 */
export default function SessionDetail({ id }: { id: string }) {
  const [session, setSession] = useState<Session | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [error, setError] = useState('');
  const [zoomed, setZoomed] = useState<SessionEvent | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const [s, e] = await Promise.all([api.getSession(id), api.listEvents(id)]);
        if (cancelled) return;
        setSession(s);
        setEvents(e.events ?? []);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    }
    void load();
    const timer = window.setInterval(load, 3000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [id]);

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle>读取会话失败</AlertTitle>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    );
  }

  if (!session) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  const asserts = events.filter((e) => e.kind === 'assert');
  const failures = events.filter((e) => !e.ok);
  const lastOffset = events.reduce((max, e) => Math.max(max, e.videoOffsetMs), 0);

  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-6">
      <header className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button variant="ghost" size="sm" render={<a href="#/sessions" />}>
            <ArrowLeftIcon data-icon="inline-start" />
            会话列表
          </Button>
          <h1 className="font-mono text-base">{session.id}</h1>
          <StatusBadge status={session.status} />
          <VerdictBadge verdict={session.verdict} />
          <Badge variant="outline">{session.app}</Badge>
          <span className="font-mono text-xs text-muted-foreground">{session.ref || '—'}</span>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>{formatAgo(session.createdAt)}</span>
          <span>{events.length} 步</span>
          {asserts.length > 0 && (
            <span>
              {asserts.length} 条断言（{asserts.filter((a) => a.ok).length} 成立）
            </span>
          )}
          {failures.length > 0 && <span className="text-destructive">{failures.length} 步失败</span>}
          {lastOffset > 0 && <span>录像 {formatClock(lastOffset / 1000)}</span>}
          {session.frame && (
            <span>
              画幅 {session.frame.width}×{session.frame.height}
            </span>
          )}
        </div>
      </header>

      {session.summary && <p className="text-sm leading-relaxed">{session.summary}</p>}

      {session.error && (
        <Alert variant="destructive">
          <AlertTitle>失败原因</AlertTitle>
          <AlertDescription>
            <pre className="font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">{session.error}</pre>
          </AlertDescription>
        </Alert>
      )}

      {events.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>还没有任何操作</EmptyTitle>
            <EmptyDescription>模型还没对这台机器做任何操作。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <ol className="divide-y divide-border/70">
          {events.map((event, index) => (
            <StepRow
              key={event.seq}
              sessionId={session.id}
              event={event}
              gapMs={gapBefore(events, index)}
              display={session.display}
              onZoom={() => setZoomed(event)}
            />
          ))}
        </ol>
      )}

      <details className="border-t pt-4">
        <summary className="cursor-pointer text-sm text-muted-foreground select-none">
          附录：完整录像与日志
        </summary>
        <div className="mt-4 flex flex-col gap-4">
          <VideoAppendix session={session} />
          <LogPanel sessionId={session.id} />
        </div>
      </details>

      {zoomed && (
        <Lightbox sessionId={session.id} event={zoomed} display={session.display} onClose={() => setZoomed(null)} />
      )}
    </div>
  );
}

/**
 * 一步：左边文字，右边画面的小循环播放。
 *
 * 片段自动播（像动图），滑出视口就暂停——一页几十步全在解码没必要。
 * 想看清楚再点它，切成大图。
 */
function StepRow({
  sessionId,
  event,
  gapMs,
  display,
  onZoom,
}: {
  sessionId: string;
  event: SessionEvent;
  gapMs: number;
  display?: Session['display'];
  onZoom: () => void;
}) {
  const [resultOpen, setResultOpen] = useState(false);
  const isAssert = event.kind === 'assert';
  const isNote = event.kind === 'note';
  const failed = !event.ok;

  return (
    <>
      {gapMs >= 15000 && (
        <li className="py-2 text-center text-[11px] text-muted-foreground/70">
          · 这之间 {formatClock(gapMs / 1000)} 没有操作（模型在思考） ·
        </li>
      )}
      <li className="grid gap-3 py-5 sm:grid-cols-[minmax(0,1fr)_260px] sm:gap-6">
        <div className="min-w-0">
          <div className="flex items-baseline gap-2">
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {String(event.seq).padStart(2, '0')}
            </span>
            <span className={`text-sm font-medium ${failed ? 'text-destructive' : ''}`}>
              {event.caption || event.tool}
            </span>
            {isAssert && (
              <span className={`inline-flex items-center gap-0.5 text-[11px] ${event.ok ? 'text-success' : 'text-destructive'}`}>
                {event.ok ? <CheckIcon className="size-3" /> : <XIcon className="size-3" />}
                {event.ok ? '成立' : '不成立'}
              </span>
            )}
            {event.videoOffsetMs >= 0 && (
              <span className="ml-auto font-mono text-[11px] text-muted-foreground tabular-nums">
                {formatClock(event.videoOffsetMs / 1000)}
              </span>
            )}
          </div>

          {event.intent && <p className="mt-1 text-xs leading-relaxed text-muted-foreground">意图：{event.intent}</p>}
          {event.expect && <p className="text-xs leading-relaxed text-muted-foreground">期望：{event.expect}</p>}

          {event.result && !isNote && (
            <div className="mt-1.5">
              <p
                className={`text-xs leading-relaxed break-words ${event.result.length > 200 && !resultOpen ? 'line-clamp-2' : ''} ${
                  failed ? 'text-destructive' : 'text-muted-foreground'
                }`}
              >
                {isAssert ? `证据：${event.result.replace(/^断言(不?)成立：/, '')}` : event.result}
              </p>
              {event.result.length > 200 && (
                <button
                  onClick={() => setResultOpen((v) => !v)}
                  className="text-[11px] text-muted-foreground/80 underline-offset-2 hover:underline"
                >
                  {resultOpen ? '收起' : '展开完整结果'}
                </button>
              )}
            </div>
          )}
        </div>

        <StepMedia sessionId={sessionId} event={event} display={display} onZoom={onZoom} />
      </li>
    </>
  );
}

/** 小窗播放：进入视口就播，离开就停；点开看大图 */
function StepMedia({
  sessionId,
  event,
  display,
  onZoom,
}: {
  sessionId: string;
  event: SessionEvent;
  display?: Session['display'];
  onZoom: () => void;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const [inView, setInView] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const observer = new IntersectionObserver(([entry]) => setInView(entry.isIntersecting), { threshold: 0.3 });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (inView) void el.play().catch(() => undefined);
    else el.pause();
  }, [inView]);

  if (!event.clip && !event.shot) return <div className="hidden sm:block" />;

  return (
    <button
      onClick={onZoom}
      className="group relative block w-full self-start overflow-hidden rounded-md bg-neutral-950 ring-1 ring-black/10 transition hover:ring-2 hover:ring-foreground/20"
      title="点开看大图"
      aria-label={`${event.caption} 的画面`}
    >
      {event.clip ? (
        // 按"这一步当时"的窗口矩形裁剪：窗口中途变过（登录后切全屏），
        // 用整场最后的几何去裁前面的片段会裁出「窗口 + 一圈黑边」
        <ScreenStage frame={event.frame} display={display} className="rounded-none border-0">
          <video
            ref={ref}
            src={`/api/sessions/${sessionId}/events/${event.seq}/clip`}
            muted
            loop
            playsInline
            preload="metadata"
            className="size-full"
          />
        </ScreenStage>
      ) : (
        <img
          src={`/api/sessions/${sessionId}/artifacts/${event.shot}`}
          alt={event.caption}
          loading="lazy"
          className="block aspect-video w-full object-cover"
        />
      )}
      <span className="pointer-events-none absolute inset-0 flex items-center justify-center opacity-0 transition group-hover:opacity-100">
        <Maximize2Icon className="size-4 text-white drop-shadow" />
      </span>
    </button>
  );
}

/** 放大：整段 4 秒带控件播放，Esc 或点背景关闭 */
function Lightbox({
  sessionId,
  event,
  display,
  onClose,
}: {
  sessionId: string;
  event: SessionEvent;
  display?: Session['display'];
  onClose: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex flex-col items-center justify-center gap-3 bg-neutral-950/85 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      {event.clip ? (
        <div className="w-[min(90vw,1100px)]" onClick={(e) => e.stopPropagation()}>
          <ScreenStage frame={event.frame} display={display}>
            <video
              src={`/api/sessions/${sessionId}/events/${event.seq}/clip`}
              autoPlay
              controls
              muted
              loop
              className="size-full"
            />
          </ScreenStage>
        </div>
      ) : event.shot ? (
        <img
          src={`/api/sessions/${sessionId}/artifacts/${event.shot}`}
          alt={event.caption}
          className="max-h-[80vh] w-auto max-w-full rounded-md"
        />
      ) : null}
      <p className="max-w-xl text-center text-xs text-neutral-300">
        {String(event.seq).padStart(2, '0')} · {event.caption}
        {event.videoOffsetMs >= 0 ? ` · ${formatClock(event.videoOffsetMs / 1000)}` : ''}
      </p>
    </div>
  );
}

/** 完整录像：报告里是附录，不是主入口 */
function VideoAppendix({ session }: { session: Session }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [playing, setPlaying] = useState(false);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);

  if (!session.hasVideo) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyTitle>还没有录像</EmptyTitle>
          <EmptyDescription>会话还在启动，或启动阶段就失败了。</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const toggle = () => {
    const video = videoRef.current;
    if (!video) return;
    if (video.paused) void video.play().catch(() => undefined);
    else video.pause();
  };

  return (
    <div className="flex flex-col gap-3">
      <ScreenStage frame={session.frame} display={session.display}>
        <video
          ref={videoRef}
          preload="metadata"
          poster={`/api/sessions/${session.id}/poster`}
          src={`/api/sessions/${session.id}/video`}
          className="size-full"
          onPlay={() => setPlaying(true)}
          onPause={() => setPlaying(false)}
          onTimeUpdate={(e) => setTime(e.currentTarget.currentTime)}
          onLoadedMetadata={(e) => setDuration(e.currentTarget.duration)}
          onEnded={() => setPlaying(false)}
        />
      </ScreenStage>
      <div className="flex items-center gap-3">
        <Button size="icon-sm" variant="ghost" onClick={toggle} aria-label={playing ? '暂停' : '播放'}>
          {playing ? <PauseIcon /> : <PlayIcon />}
        </Button>
        <span className="w-10 shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground">
          {formatClock(time)}
        </span>
        <input
          type="range"
          min={0}
          max={duration || 0}
          step={0.1}
          value={Math.min(time, duration || 0)}
          onChange={(e) => {
            const video = videoRef.current;
            if (!video) return;
            video.currentTime = Number(e.target.value);
            setTime(video.currentTime);
          }}
          className="h-1 min-w-0 flex-1 accent-foreground"
          aria-label="录像进度"
        />
        <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">{formatClock(duration)}</span>
      </div>
    </div>
  );
}

/**
 * 日志面板：容器输出（应用为什么没起来通常只在这里）与应用自身日志切换查看。
 *
 * 容器是一次性的，调度器会在删除容器前把 stdout/stderr 归档进会话目录，
 * 所以这里读的是归档文件，不是 docker logs。
 */
function LogPanel({ sessionId }: { sessionId: string }) {
  const [sources, setSources] = useState<LogSource[]>([]);
  const [active, setActive] = useState('container');
  const [text, setText] = useState('');

  useEffect(() => {
    fetch(`/api/sessions/${sessionId}/logs`)
      .then((resp) => (resp.ok ? resp.json() : { sources: [] }))
      .then((data: { sources: LogSource[] }) => {
        setSources(data.sources ?? []);
        if (data.sources?.length && !data.sources.some((s) => s.name === active)) {
          setActive(data.sources[0].name);
        }
      })
      .catch(() => setSources([]));
    // active 变化时需要重新取，故依赖里带上它
  }, [sessionId, active]);

  useEffect(() => {
    let alive = true;
    fetch(`/api/sessions/${sessionId}/logs/${active}?tail=200`)
      .then((resp) => (resp.ok ? resp.text() : ''))
      .then((body) => {
        if (alive) setText(body);
      })
      .catch(() => setText(''));
    return () => {
      alive = false;
    };
  }, [sessionId, active, sources.length]);

  if (sources.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>日志</CardTitle>
        </CardHeader>
        <CardContent className="text-xs text-muted-foreground">暂无日志</CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>日志</CardTitle>
        <CardAction>
          <Button
            variant="ghost"
            size="xs"
            render={<a href={`/api/sessions/${sessionId}/logs/${active}`} target="_blank" rel="noreferrer" />}
          >
            全文
            <ExternalLinkIcon data-icon="inline-end" />
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        <Tabs value={active} onValueChange={(value) => setActive(String(value))}>
          <TabsList className="mb-2 flex-wrap">
            {sources.map((source) => (
              <TabsTrigger key={source.name} value={source.name}>
                {source.label}
              </TabsTrigger>
            ))}
          </TabsList>
          <TabsContent value={active}>
            <pre className="max-h-72 overflow-auto rounded-md bg-neutral-950 p-3 text-[11px] leading-relaxed text-neutral-200">
              {formatLog(text) || '（空）'}
            </pre>
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}

/** 与上一步之间的空窗（毫秒）：报告的节奏靠它，不靠录像 */
function gapBefore(events: SessionEvent[], index: number): number {
  // 第一步之前不标空窗：那段是启动 + 模型第一次思考，没有参照点
  if (index === 0) return 0;
  const prev = new Date(events[index - 1].at).getTime();
  const cur = new Date(events[index].at).getTime();
  return Math.max(0, cur - prev);
}

/** 秒数 → m:ss */
function formatClock(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00';
  const total = Math.floor(seconds);
  const s = String(total % 60).padStart(2, '0');
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

/**
 * 把日志压成一眼能扫的形态。
 *
 * 应用日志常见是有字段名的 JSONL（time/level/module/message），原样贴出来满屏都是花括号；
 * 这里把每行折成「时间 级别 [模块] 消息」。解析失败的行原样保留——不能为了让界面好看而丢信息。
 */
function formatLog(raw: string): string {
  return raw
    .split('\n')
    .map((line) => {
      const trimmed = line.trim();
      if (!trimmed.startsWith('{')) return trimmed;
      try {
        const entry = JSON.parse(trimmed) as Record<string, unknown>;
        const time = typeof entry.time === 'string' ? entry.time.slice(11, 19) : '';
        const level = String(entry.level ?? '').toUpperCase().padEnd(5);
        const module = entry.module ?? entry.name ?? entry.scope ?? '';
        const msg = entry.message ?? entry.msg ?? '';
        const rest = { ...entry };
        delete rest.schemaVersion;
        delete rest.appVersion;
        delete rest.runId;
        delete rest.time;
        delete rest.level;
        delete rest.message;
        delete rest.msg;
        delete rest.module;
        delete rest.name;
        delete rest.scope;
        const extra = Object.keys(rest).length ? ` ${JSON.stringify(rest)}` : '';
        return `${time} ${level} ${module ? `[${module}] ` : ''}${msg}${extra}`;
      } catch {
        return trimmed;
      }
    })
    .join('\n');
}
