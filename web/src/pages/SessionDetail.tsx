import { useEffect, useRef, useState } from 'react';
import { ArrowLeftIcon, CheckIcon, ExternalLinkIcon, PauseIcon, PlayIcon, XIcon } from 'lucide-react';
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
 * 阅读路径是「步骤流」而不是「一条长录像」：每一步做了什么、为什么做、期望什么、
 * 实际结果是什么、当时屏幕长什么样，都排在一列里往下读。录像退到附录，
 * 每一步旁边那个 ▶ 才是它的入口——点开只看这 4 秒，不用拖进度条跳过模型的思考时间。
 */
export default function SessionDetail({ id }: { id: string }) {
  const [session, setSession] = useState<Session | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [error, setError] = useState('');

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
    <div className="flex flex-col gap-5">
      <header className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button variant="ghost" size="sm" render={<a href="#/sessions" />}>
            <ArrowLeftIcon data-icon="inline-start" />
            会话列表
          </Button>
          <h1 className="font-mono text-lg">{session.id}</h1>
          <StatusBadge status={session.status} />
          <VerdictBadge verdict={session.verdict} />
          <Badge variant="outline">{session.app}</Badge>
          <span className="font-mono text-xs text-muted-foreground">{session.ref || '—'}</span>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>创建 {formatAgo(session.createdAt)}</span>
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
          <span>调试端口 {session.cdpPort}</span>
        </div>
      </header>

      {session.summary && (
        <Card>
          <CardContent className="text-sm leading-relaxed">{session.summary}</CardContent>
        </Card>
      )}

      {session.error && (
        <Alert variant="destructive">
          <AlertTitle>失败原因</AlertTitle>
          <AlertDescription>
            <pre className="font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">{session.error}</pre>
          </AlertDescription>
        </Alert>
      )}

      <section className="flex flex-col gap-1">
        <h2 className="text-sm font-medium">验收过程</h2>
        <p className="text-xs text-muted-foreground">
          按发生顺序排列。▶ 是这一步前后 4 秒的画面，完整录像在页面最下面的附录里。
        </p>
      </section>

      {events.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>还没有任何操作</EmptyTitle>
            <EmptyDescription>模型还没对这台机器做任何操作。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <ol className="flex flex-col">
          {events.map((event, index) => (
            <StepRow key={event.seq} sessionId={session.id} event={event} gapMs={gapBefore(events, index)} />
          ))}
        </ol>
      )}

      <details className="rounded-lg border" open={events.length === 0}>
        <summary className="cursor-pointer px-4 py-3 text-sm font-medium select-none">
          附录：完整录像与日志
          <span className="ml-2 text-xs font-normal text-muted-foreground">
            {session.hasVideo ? '整段录像（含模型的思考空窗）' : '还没有录像'}
          </span>
        </summary>
        <div className="flex flex-col gap-4 border-t p-4">
          <VideoAppendix session={session} />
          <LogPanel sessionId={session.id} />
        </div>
      </details>
    </div>
  );
}

/**
 * 一步：序号 + 描述 + 意图/期望 + 结果 + 画面。
 *
 * 断言步和动作步用不同的左边框颜色区分——看一眼就知道哪几条是「结论」。
 */
function StepRow({ sessionId, event, gapMs }: { sessionId: string; event: SessionEvent; gapMs: number }) {
  const [expanded, setExpanded] = useState(false);
  const [resultOpen, setResultOpen] = useState(false);
  const isAssert = event.kind === 'assert';
  const failed = !event.ok;

  return (
    <>
      {gapMs >= 15000 && (
        // 空窗不删掉，而是标出来：模型在这段时间里没操作屏幕，但它在思考。
        <li className="ml-4 flex items-center gap-2 border-l border-dashed py-1 pl-4 text-xs text-muted-foreground">
          <span className="size-1 rounded-full bg-muted-foreground/50" />
          这之间 {formatClock(gapMs / 1000)} 没有操作（模型在思考）
        </li>
      )}
      <li className="flex gap-3 pb-3">
        <div className="flex w-6 shrink-0 flex-col items-center pt-2">
          <span
            className={`flex size-6 items-center justify-center rounded-full border font-mono text-[11px] ${
              failed ? 'border-destructive text-destructive' : 'text-muted-foreground'
            }`}
          >
            {event.seq}
          </span>
          <span className="mt-1 w-px flex-1 bg-border" />
        </div>

        <div
          className={`min-w-0 flex-1 rounded-lg border p-3 ${
            failed ? 'border-destructive/40 bg-destructive/5' : isAssert ? 'bg-muted/40' : ''
          }`}
        >
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="text-sm font-medium">{event.caption || event.tool}</span>
              {isAssert && (
                <Badge variant={event.ok ? 'success' : 'destructive'}>
                  {event.ok ? (
                    <>
                      <CheckIcon data-icon="inline-start" />
                      成立
                    </>
                  ) : (
                    <>
                      <XIcon data-icon="inline-start" />
                      不成立
                    </>
                  )}
                </Badge>
              )}
            </div>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="font-mono">{event.videoOffsetMs >= 0 ? formatClock(event.videoOffsetMs / 1000) : '—'}</span>
              {event.clip && (
                <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
                  {expanded ? <PauseIcon data-icon="inline-start" /> : <PlayIcon data-icon="inline-start" />}
                  {expanded ? '收起' : '看这 4 秒'}
                </Button>
              )}
            </div>
          </div>

          {event.intent && <p className="mt-1 text-xs text-muted-foreground">意图：{event.intent}</p>}
          {event.expect && <p className="mt-0.5 text-xs text-muted-foreground">期望：{event.expect}</p>}

          {event.result && (
            <div className="mt-1">
              <p
                className={`text-xs break-words ${event.result.length > 200 && !resultOpen ? 'line-clamp-2' : ''} ${
                  failed ? 'text-destructive' : 'text-muted-foreground'
                }`}
              >
                {isAssert ? `证据：${event.result.replace(/^断言(不?)成立：/, '')}` : event.result}
              </p>
              {event.result.length > 200 && (
                <button
                  onClick={() => setResultOpen((v) => !v)}
                  className="text-[11px] text-muted-foreground underline-offset-2 hover:underline"
                >
                  {resultOpen ? '收起结果' : `展开完整结果（${event.result.length} 字）`}
                </button>
              )}
            </div>
          )}

          {expanded && event.clip && (
            <video
              autoPlay
              controls
              preload="none"
              src={`/api/sessions/${sessionId}/events/${event.seq}/clip`}
              className="mt-2 w-full rounded-md bg-neutral-950"
            />
          )}

          {event.shot && !expanded && (
            // 缩略图按原始比例给高上限，不拉伸：报告里一屏能扫到几步
            <button
              onClick={() => setExpanded(true)}
              className="mt-2 block w-fit max-w-full cursor-zoom-in overflow-hidden rounded-md border bg-neutral-950"
              title="点开看这 4 秒"
            >
              <img
                src={`/api/sessions/${sessionId}/artifacts/${event.shot}`}
                alt={event.caption}
                loading="lazy"
                className="block max-h-[280px] w-auto max-w-full"
              />
            </button>
          )}

          {!event.shot && event.artifact && (
            <p className="mt-2 text-xs text-muted-foreground">
              附件：{event.artifact}（{event.ok ? '已落盘' : '未生成'}）
            </p>
          )}
        </div>
      </li>
    </>
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
