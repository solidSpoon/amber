import { useEffect, useRef, useState } from 'react';
import { ArrowLeftIcon, ExternalLinkIcon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Skeleton } from '@/components/ui/skeleton';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { StatusBadge, VerdictBadge } from '@/components/session-badges';
import { api, formatAgo, type LogSource, type Session, type SessionEvent } from '../api';

/**
 * 会话详情：录像 + 操作时间线 + 日志。
 *
 * 核心交互是「点操作 → 录像跳到那一刻」：两边用的是同一个基准（录像起点），
 * 所以这里直接用后端给的 videoOffsetMs，不再二次换算。
 */
export default function SessionDetail({ id }: { id: string }) {
  const [session, setSession] = useState<Session | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<number | null>(null);
  const videoRef = useRef<HTMLVideoElement>(null);

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

  function seek(event: SessionEvent) {
    setSelected(event.seq);
    const video = videoRef.current;
    if (!video || event.videoOffsetMs < 0) return;
    video.currentTime = event.videoOffsetMs / 1000;
    void video.play().catch(() => undefined);
  }

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

  const screenshots = events.filter((e) => e.artifact?.startsWith('screenshots/'));

  return (
    <div className="flex flex-col gap-4">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button variant="ghost" size="sm" render={<a href="#/sessions" />}>
          <ArrowLeftIcon data-icon="inline-start" />
          会话列表
        </Button>
        <h1 className="font-mono text-lg">{session.id}</h1>
        <StatusBadge status={session.status} />
        <VerdictBadge verdict={session.verdict} />
        <Badge variant="outline">{session.app}</Badge>
        <span className="font-mono text-xs text-muted-foreground">{session.ref || '—'}</span>
        <span className="text-xs text-muted-foreground">
          创建 {formatAgo(session.createdAt)} · 调试端口 {session.cdpPort}
          {session.videoDurationMs > 0 ? ` · 录像 ${(session.videoDurationMs / 1000).toFixed(1)}s` : ''}
        </span>
      </header>

      {session.summary && (
        <Card>
          <CardContent className="text-sm">{session.summary}</CardContent>
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

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <div className="flex min-w-0 flex-col gap-4">
          <Card>
            <CardContent>
              {session.hasVideo ? (
                <video
                  ref={videoRef}
                  controls
                  preload="metadata"
                  src={`/api/sessions/${session.id}/video`}
                  className="w-full rounded-md bg-neutral-950"
                />
              ) : (
                <Empty>
                  <EmptyHeader>
                    <EmptyTitle>还没有录像</EmptyTitle>
                    <EmptyDescription>会话仍在运行，或启动阶段就失败了。</EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )}
            </CardContent>
          </Card>

          {screenshots.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>截图证据</CardTitle>
                <CardDescription>点一张跳到录像里的对应时刻。</CardDescription>
              </CardHeader>
              <CardContent className="grid grid-cols-2 gap-2 md:grid-cols-3">
                {screenshots.map((e) => (
                  <button
                    key={e.seq}
                    onClick={() => seek(e)}
                    className="group overflow-hidden rounded-md border text-left transition hover:bg-muted"
                  >
                    <img
                      src={`/api/sessions/${session.id}/artifacts/${e.artifact!.split('/').pop()}`}
                      alt={e.artifact}
                      className="aspect-video w-full object-cover"
                    />
                    <div className="truncate px-2 py-1 text-xs text-muted-foreground">{e.artifact}</div>
                  </button>
                ))}
              </CardContent>
            </Card>
          )}
        </div>

        <div className="flex min-w-0 flex-col gap-4">
          <Card>
            <CardHeader>
              <CardTitle>操作时间线</CardTitle>
              <CardDescription>点一步跳一段录像。</CardDescription>
            </CardHeader>
            <CardContent>
              {events.length === 0 ? (
                <Empty>
                  <EmptyHeader>
                    <EmptyTitle>还没有工具调用</EmptyTitle>
                    <EmptyDescription>模型还没对这台机器做任何操作。</EmptyDescription>
                  </EmptyHeader>
                </Empty>
              ) : (
                <ScrollArea className="max-h-[420px]">
                  <ol className="flex flex-col gap-1 pr-3">
                    {events.map((e) => (
                      <li key={e.seq}>
                        <button
                          onClick={() => seek(e)}
                          className={`w-full rounded-md border px-3 py-2 text-left text-xs transition hover:bg-muted ${
                            selected === e.seq ? 'border-ring bg-muted' : 'border-border'
                          }`}
                        >
                          <div className="flex items-center justify-between gap-2">
                            <span className="font-medium">{e.tool}</span>
                            <span className={e.ok ? 'text-muted-foreground' : 'text-destructive'}>
                              {e.videoOffsetMs >= 0 ? `${(e.videoOffsetMs / 1000).toFixed(1)}s` : '—'}
                            </span>
                          </div>
                          <div className="mt-1 truncate text-muted-foreground">{e.result || '(无结果文本)'}</div>
                        </button>
                      </li>
                    ))}
                  </ol>
                </ScrollArea>
              )}
            </CardContent>
          </Card>

          <LogPanel sessionId={session.id} />
        </div>
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
          <Button variant="ghost" size="xs" render={<a href={`/api/sessions/${sessionId}/logs/${active}`} target="_blank" rel="noreferrer" />}>
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
