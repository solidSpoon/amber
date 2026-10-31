import { useEffect, useRef, useState, type ReactNode } from 'react';
import { ArrowLeftIcon, ExternalLinkIcon, Maximize2Icon } from 'lucide-react';
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
 * 会话详情 = 这次会话留下的记录。
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

  const failures = events.filter((e) => !e.ok);
  const lastOffset = events.reduce((max, e) => Math.max(max, e.videoOffsetMs), 0);

  return (
    // 详情页的主体是画面证据：栏宽给到 1440，画面列再单独放大，
    // 否则在宽屏上这一段只有一张邮票大，读者得点开才能看清发生了什么。
    <div className="mx-auto flex w-full max-w-[88rem] flex-col gap-5 sm:gap-6">
      <header className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button variant="ghost" size="sm" render={<a href="#/sessions" />}>
            <ArrowLeftIcon data-icon="inline-start" />
            会话列表
          </Button>
          {/* 标题就是这次会话的意图；会话 ID 退到下面一行，读者不必靠它认人 */}
          <h1 className="text-base font-medium">{session.title || session.app}</h1>
          <StatusBadge status={session.status} />
          <VerdictBadge verdict={session.verdict} />
          <Badge variant="outline">{session.app}</Badge>
          <span className="font-mono text-xs text-muted-foreground">{session.ref || session.id}</span>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>{formatAgo(session.createdAt)}</span>
          <span>{events.length} 步</span>
          {failures.length > 0 && <span className="text-destructive">{failures.length} 步失败</span>}
          {lastOffset > 0 && <span>时长 {formatClock(lastOffset / 1000)}</span>}
          {session.display && (
            <span>
              屏幕 {session.display.width}×{session.display.height}
            </span>
          )}
          {session.frame && (
            <span>
              应用窗口 {session.frame.width}×{session.frame.height}
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
        <ol className="flex flex-col divide-y divide-border/70">
          {batchGroups(events).map((group, index) => (
            <BatchRow
              key={group.start}
              index={index + 1}
              sessionId={session.id}
              group={group}
              display={session.display}
              onZoom={setZoomed}
            />
          ))}
        </ol>
      )}

      <details className="border-t pt-4">
        <summary className="cursor-pointer text-sm text-muted-foreground select-none">附录：容器与应用日志</summary>
        <div className="mt-4">
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
 * 画面外框：点开看大图的那层壳，所有画面都走它，样式只写一份。
 *
 * 不配图注：左边那段文字已经把它是什么说完了（截图是这一批的结论帧、片段是这一段的过程），
 * 再在图片下面挂一行"这一步的画面"只是把同一句话说两遍。
 */
function MediaTile({ onZoom, children }: { onZoom: () => void; children: ReactNode }) {
  return (
    <figure>
      <button
        onClick={onZoom}
        className="group relative block w-full cursor-zoom-in overflow-hidden rounded-md bg-neutral-950 ring-1 ring-black/10 transition hover:ring-2 hover:ring-foreground/20"
        title="点开看大图"
        aria-label="点开看大图"
      >
        {children}
        <span className="pointer-events-none absolute inset-0 flex items-center justify-center opacity-0 transition group-hover:opacity-100">
          <Maximize2Icon className="size-4 text-white drop-shadow" />
        </span>
      </button>
    </figure>
  );
}

/**
 * 这一段的画面：自动循环播放（像动图），滑出视口就暂停——一页几十步全在解码没必要。
 * 它是"这几秒的过程"，不是某一步的结果；整场录像不留，所以这里就是读者能看到的动态画面。
 */
function ClipPlayer({
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
  const [progress, setProgress] = useState(0);

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

  return (
    <MediaTile onZoom={onZoom}>
      <ScreenStage display={display} className="rounded-none border-0">
        <video
          // key 带上 seq：不然 React 会复用同一个 video 元素，切换批次时还放着上一段的画面
          key={`clip-${event.seq}`}
          ref={ref}
          src={`/api/sessions/${sessionId}/events/${event.seq}/clip`}
          muted
          loop
          playsInline
          preload="metadata"
          className="size-full"
          onTimeUpdate={(e) => {
            const video = e.currentTarget;
            setProgress(video.duration ? video.currentTime / video.duration : 0);
          }}
        />
      </ScreenStage>
      {/* 进度条跟着片段循环走：一眼看出片段放到了哪，不用等它播完 */}
      <span className="pointer-events-none absolute inset-x-0 bottom-0 h-[3px] bg-black/40">
        <span
          className="block h-full bg-white shadow-[0_0_2px_rgba(0,0,0,0.6)] transition-[width] duration-150 ease-linear"
          style={{ width: `${Math.min(100, progress * 100)}%` }}
        />
      </span>
    </MediaTile>
  );
}

/**
 * 一张截图：capture 交回去的那一帧，或者失败那一步的现场。
 *
 * 交付物本身就是整屏图（含原生对话框），所以按原样显示、不裁剪——
 * 读者要看的正是"当时屏幕长什么样"。
 */
function ShotTile({
  sessionId,
  event,
  onZoom,
}: {
  sessionId: string;
  event: SessionEvent;
  onZoom: () => void;
}) {
  if (!event.shot) return null;
  // 截图就是这一批收尾时交回的那一帧，放在文字下面；不再配一行图注说明它是什么
  return (
    <MediaTile onZoom={onZoom}>
      <img
        src={`/api/sessions/${sessionId}/artifacts/${event.shot}`}
        alt={event.caption}
        loading="lazy"
        className="block w-full"
      />
    </MediaTile>
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
          <ScreenStage display={display}>
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


/**
 * 一批 = 记录里的一段。
 *
 * 版式：左边是「这一批做了什么」，右边是「当时的样子」。
 * 桌面左右两栏——一屏能同时读到文字和画面，眼睛不用来回上下跳；
 * 窄屏自动堆成一栏，画面落在这段文字下面。
 *
 * 截图放在左边文字下面（run 收尾交回的那一帧），右边只放这一段的画面。
 * 两者不是一回事：片段是「这几秒的过程」，截图是「这一批结束时的那一帧」。
 */
function BatchRow({
  index,
  sessionId,
  group,
  display,
  onZoom,
}: {
  index: number;
  sessionId: string;
  group: { start: number; events: SessionEvent[] };
  display?: Session['display'];
  onZoom: (event: SessionEvent) => void;
}) {
  const failed = group.events.filter((e) => !e.ok);
  // 一批只放一条录像片段：批内相邻两步可能只隔 0.2 秒，逐行放就成了同一段画面循环十几遍。
  const clipEvent = group.events.find((e) => e.clip);
  // 一批只贴一张图：就是这一批收尾时交回来的那一帧（run 的最后一步是 wait_and_capture）。
  // 批内其他步骤的截图是过程，逐张铺开会把"这批的结论"淹掉。
  const shots = pickKeyShot(group.events);
  const end = Math.max(0, group.events[group.events.length - 1].videoOffsetMs);

  return (
    // 窄屏是一列：文字 → 截图 → 这一段的画面。手机上两张全宽的图会把一屏塞满，
    // 所以截图在这里收窄成"证据缩略图"，主画面（片段）才占满宽。
    <li className="grid gap-3 py-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,28rem)] sm:gap-8 sm:py-5 xl:grid-cols-[minmax(0,1fr)_minmax(0,42rem)]">
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
          <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
            {String(index).padStart(2, '0')}
          </span>
          {/* 这一段的标题：调用方给的意图；没给就退回按动作拼（绝不编） */}
          <span className={`text-sm font-medium ${failed.length ? 'text-destructive' : ''}`}>
            {group.events[0].batchIntent || summarizeBatch(group.events)}
          </span>
          <span className="ml-auto font-mono text-[11px] text-muted-foreground tabular-nums">
            {formatClock(Math.max(0, group.start) / 1000)}
            {end > group.start + 500 && `–${formatClock(end / 1000)}`}
          </span>
        </div>

        {/* 批内明细：一行一步，只写做什么和结果，不铺开意图/期望 */}
        <ul className="flex flex-col gap-0.5 pl-4 text-[11px] text-muted-foreground/80 sm:pl-6">
          {group.events.filter((e) => !isSleepStep(e)).map((event) => (
            <li key={event.seq} className="flex items-baseline gap-2">
              <span className="text-muted-foreground/50">·</span>
              <span className={event.ok ? '' : 'text-destructive'}>{event.caption}</span>
              {resultHead(event.result) && (
                <span className="truncate text-muted-foreground/60">{resultHead(event.result)}</span>
              )}
            </li>
          ))}
        </ul>

        {/* 截图贴在文字下面：先读完"做了什么"，紧跟着就是"当时交回去的那一帧"。
            比右栏的录像略小一圈——它是证据，不是主画面。 */}
        {shots.length > 0 && (
          <div className="mt-1 flex max-w-[68%] flex-col gap-2 pl-4 sm:mt-0 sm:max-w-[22rem] sm:pl-6">
            {shots.map((event) => (
              <ShotTile
                key={event.seq}
                sessionId={sessionId}
                event={event}
                onZoom={() => onZoom(event)}
              />
            ))}
          </div>
        )}
      </div>

      <div className="flex min-w-0 flex-col gap-2 sm:self-start">
        {clipEvent && (
          <ClipPlayer
            sessionId={sessionId}
            event={clipEvent}
            display={display}
            onZoom={() => onZoom(clipEvent)}
          />
        )}
      </div>
    </li>
  );
}

/** 一批的代表帧：capture 交回的那张；没有就退到批内最后一张截图 */
function pickKeyShot(events: SessionEvent[]): SessionEvent[] {
  const shots = events.filter((e) => e.shot);
  if (shots.length === 0) return [];
  const capture = shots.find((e) => e.tool === 'capture');
  return [capture ?? shots[shots.length - 1]];
}

/** 纯等待不产生新画面，记录里并进相邻的动作，不单独占一行 */
function isSleepStep(e: SessionEvent): boolean {
  return e.tool === 'sleep' || (e.tool === 'eval' && /new Promise\([^)]*setTimeout/.test(JSON.stringify(e.args ?? '')));
}

/** 一批的标题：用批里的动作名拼一句话，读者先看这句就够 */
function summarizeBatch(events: SessionEvent[]): string {
  const names = events.filter((e) => e.kind !== 'note' && !isSleepStep(e)).map((e) => e.caption.replace(/^点击 /, ''));
  if (names.length === 0) return '一批操作';
  const head = names.slice(0, 3).join(' → ');
  return names.length > 3 ? `${head} 等 ${names.length} 个动作` : head;
}

/** 按「一段连续操作」分组：记录里同批的动作归在一起，一批只有一条片段 */
function batchGroups(events: SessionEvent[]): { start: number; events: SessionEvent[] }[] {
  const groups: { start: number; events: SessionEvent[] }[] = [];
  for (const e of events) {
    const last = groups[groups.length - 1];
    if (last && last.start === e.batchStartMs) {
      last.events.push(e);
      continue;
    }
    groups.push({ start: e.batchStartMs, events: [e] });
  }
  return groups;
}

/** 结果的第一行 = 这一步的结论 */
function resultHead(text: string): string {
  return text.split('\n').find((line) => line.trim() !== '')?.trim() ?? '';
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
