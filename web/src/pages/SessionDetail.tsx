import { useEffect, useRef, useState } from 'react';
import { api, formatAgo, type LogSource, type Session, type SessionEvent } from '../api';

/**
 * 会话详情：左边录像，右边每一步操作。
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

  if (error) return <p className="text-red-600">{error}</p>;
  if (!session) return <p className="text-neutral-400">加载中…</p>;

  const screenshots = events.filter((e) => e.artifact?.startsWith('screenshots/'));

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <a href="#/sessions" className="text-sm text-neutral-500 hover:text-neutral-900">
          ← 会话列表
        </a>
        <h1 className="font-mono text-lg">{session.id}</h1>
        <span className="text-sm text-neutral-600">
          {session.app} @ {session.ref || '—'} · {session.status}
          {session.verdict ? ` · ${session.verdict}` : ''}
        </span>
        <span className="text-sm text-neutral-500">
          创建 {formatAgo(session.createdAt)} · 调试端口 {session.cdpPort}
          {session.videoDurationMs > 0 ? ` · 录像 ${(session.videoDurationMs / 1000).toFixed(1)}s` : ''}
        </span>
      </header>

      {session.summary && (
        <p className="rounded-md border border-neutral-200 bg-white px-4 py-2 text-sm text-neutral-700">
          {session.summary}
        </p>
      )}

      {session.error && (
        <div className="rounded-md border border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700">
          <div className="mb-1 text-xs font-medium text-red-500">失败原因</div>
          <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">{session.error}</pre>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="space-y-4">
          {session.hasVideo ? (
            <video
              ref={videoRef}
              controls
              preload="metadata"
              src={`/api/sessions/${session.id}/video`}
              className="w-full rounded-lg border border-neutral-300 bg-black"
            />
          ) : (
            <div className="flex h-56 items-center justify-center rounded-lg border border-neutral-200 bg-white text-sm text-neutral-400">
              该会话还没有录像（会话可能仍在运行，或启动阶段就失败了）
            </div>
          )}

          {screenshots.length > 0 && (
            <div className="space-y-2">
              <h2 className="text-sm font-medium text-neutral-600">截图证据</h2>
              <div className="grid grid-cols-2 gap-2 md:grid-cols-3">
                {screenshots.map((e) => (
                  <button
                    key={e.seq}
                    onClick={() => seek(e)}
                    className="group overflow-hidden rounded-md border border-neutral-200 bg-white text-left"
                  >
                    <img
                      src={`/api/sessions/${session.id}/artifacts/${e.artifact!.split('/').pop()}`}
                      alt={e.artifact}
                      className="aspect-video w-full object-cover transition group-hover:opacity-80"
                    />
                    <div className="truncate px-2 py-1 text-xs text-neutral-400">{e.artifact}</div>
                  </button>
                ))}
              </div>
            </div>
          )}
        </section>

        <aside className="min-w-0 space-y-4">
          <h2 className="mb-2 text-sm font-medium text-neutral-600">操作时间线（点一步跳一段录像）</h2>
          <ol className="space-y-1">
            {events.length === 0 && <li className="text-sm text-neutral-400">还没有工具调用。</li>}
            {events.map((e) => (
              <li key={e.seq}>
                <button
                  onClick={() => seek(e)}
                  className={`w-full rounded-md border px-3 py-2 text-left text-xs transition ${
                    selected === e.seq
                      ? 'border-sky-400 bg-sky-50'
                      : 'border-neutral-200 bg-white hover:border-neutral-300 hover:bg-neutral-50'
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-medium text-neutral-900">{e.tool}</span>
                    <span className={e.ok ? 'text-neutral-500' : 'text-red-400'}>
                      {e.videoOffsetMs >= 0 ? `${(e.videoOffsetMs / 1000).toFixed(1)}s` : '—'}
                    </span>
                  </div>
                  <div className="mt-1 truncate text-neutral-400">{e.result || '(无结果文本)'}</div>
                </button>
              </li>
            ))}
          </ol>

          <LogPanel sessionId={session.id} />
        </aside>
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
    return <p className="text-xs text-neutral-400">暂无日志</p>;
  }

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1">
        {sources.map((source) => (
          <button
            key={source.name}
            onClick={() => setActive(source.name)}
            className={`rounded-md px-2 py-1 text-xs transition ${
              active === source.name
                ? 'bg-neutral-900 text-white'
                : 'bg-neutral-100 text-neutral-600 hover:bg-neutral-200'
            }`}
          >
            {source.label}
          </button>
        ))}
        <a
          href={`/api/sessions/${sessionId}/logs/${active}`}
          target="_blank"
          rel="noreferrer"
          className="ml-auto text-xs text-sky-600 hover:underline"
        >
          全文 ↗
        </a>
      </div>
      <pre className="max-h-72 overflow-auto rounded-lg bg-neutral-950 p-3 text-[11px] leading-relaxed text-neutral-200">
        {formatLog(text) || '（空）'}
      </pre>
    </div>
  );
}


/**
 * 把日志压成一眼能扫的形态。
 *
 * DashPlayer 的应用日志是 JSONL，原样贴出来满屏都是 {"schemaVersion":1,…}；
 * 这里把每行折成「时间 级别 [模块] 消息」，解析失败的行原样保留（不能丢信息）。
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
