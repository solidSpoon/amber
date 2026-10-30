import { useEffect, useRef, useState } from 'react';
import { api, formatAgo, type Session, type SessionEvent } from '../api';

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

  if (error) return <p className="text-red-400">{error}</p>;
  if (!session) return <p className="text-neutral-500">加载中…</p>;

  const screenshots = events.filter((e) => e.artifact?.startsWith('screenshots/'));

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <a href="#/sessions" className="text-sm text-neutral-500 hover:text-neutral-300">
          ← 会话列表
        </a>
        <h1 className="font-mono text-lg">{session.id}</h1>
        <span className="text-sm text-neutral-400">
          {session.app} @ {session.ref || '—'} · {session.status}
          {session.verdict ? ` · ${session.verdict}` : ''}
        </span>
        <span className="text-sm text-neutral-500">
          创建 {formatAgo(session.createdAt)} · 调试端口 {session.cdpPort}
          {session.videoDurationMs > 0 ? ` · 录像 ${(session.videoDurationMs / 1000).toFixed(1)}s` : ''}
        </span>
      </header>

      {session.summary && (
        <p className="rounded-md border border-neutral-800 bg-neutral-900/40 px-4 py-2 text-sm text-neutral-300">
          {session.summary}
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="space-y-4">
          {session.hasVideo ? (
            <video
              ref={videoRef}
              controls
              preload="metadata"
              src={`/api/sessions/${session.id}/video`}
              className="w-full rounded-lg border border-neutral-800 bg-black"
            />
          ) : (
            <div className="flex h-56 items-center justify-center rounded-lg border border-neutral-800 text-sm text-neutral-500">
              该会话还没有录像（会话可能仍在运行，或启动阶段就失败了）
            </div>
          )}

          {screenshots.length > 0 && (
            <div className="space-y-2">
              <h2 className="text-sm font-medium text-neutral-400">截图证据</h2>
              <div className="grid grid-cols-2 gap-2 md:grid-cols-3">
                {screenshots.map((e) => (
                  <button
                    key={e.seq}
                    onClick={() => seek(e)}
                    className="group overflow-hidden rounded-md border border-neutral-800 text-left"
                  >
                    <img
                      src={`/api/sessions/${session.id}/artifacts/${e.artifact!.split('/').pop()}`}
                      alt={e.artifact}
                      className="aspect-video w-full object-cover transition group-hover:opacity-80"
                    />
                    <div className="truncate px-2 py-1 text-xs text-neutral-500">{e.artifact}</div>
                  </button>
                ))}
              </div>
            </div>
          )}
        </section>

        <aside className="min-w-0">
          <h2 className="mb-2 text-sm font-medium text-neutral-400">操作时间线（点一步跳一段录像）</h2>
          <ol className="space-y-1">
            {events.length === 0 && <li className="text-sm text-neutral-500">还没有工具调用。</li>}
            {events.map((e) => (
              <li key={e.seq}>
                <button
                  onClick={() => seek(e)}
                  className={`w-full rounded-md border px-3 py-2 text-left text-xs transition ${
                    selected === e.seq
                      ? 'border-sky-500/60 bg-sky-500/10'
                      : 'border-neutral-800 hover:border-neutral-700 hover:bg-neutral-900/60'
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-medium text-neutral-200">{e.tool}</span>
                    <span className={e.ok ? 'text-neutral-500' : 'text-red-400'}>
                      {e.videoOffsetMs >= 0 ? `${(e.videoOffsetMs / 1000).toFixed(1)}s` : '—'}
                    </span>
                  </div>
                  <div className="mt-1 truncate text-neutral-500">{e.result || '(无结果文本)'}</div>
                </button>
              </li>
            ))}
          </ol>
        </aside>
      </div>
    </div>
  );
}
