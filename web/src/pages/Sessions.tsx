import { useCallback, useEffect, useState } from 'react';
import { api, formatAgo, type Session } from '../api';

const STATUS_STYLE: Record<string, string> = {
  ready: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
  starting: 'bg-sky-500/15 text-sky-300 border-sky-500/30',
  stopping: 'bg-amber-500/15 text-amber-300 border-amber-500/30',
  finished: 'bg-neutral-500/15 text-neutral-300 border-neutral-500/30',
  failed: 'bg-red-500/15 text-red-300 border-red-500/30',
  abandoned: 'bg-purple-500/15 text-purple-300 border-purple-500/30',
};

const VERDICT_STYLE: Record<string, string> = {
  pass: 'text-emerald-400',
  fail: 'text-red-400',
  inconclusive: 'text-amber-400',
};

/** 会话列表：新建、查看、结束。列表通过 SSE 自动刷新，不用手动点。 */
export default function Sessions() {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [app, setApp] = useState('DashPlayer');
  const [ref, setRef] = useState('main');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const data = await api.listSessions();
      setSessions(data.sessions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
    const source = new EventSource('/api/sessions/stream');
    source.onmessage = (event) => {
      try {
        const payload = JSON.parse(event.data) as { sessions: Session[] };
        setSessions(payload.sessions ?? []);
      } catch {
        // 单帧坏了不影响后续
      }
    };
    return () => source.close();
  }, [load]);

  async function onCreate() {
    setBusy(true);
    setError('');
    try {
      await api.createSession(app, ref);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function onStop(id: string) {
    try {
      await api.stopSession(id, 'inconclusive', '管理台手动结束');
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold">验收会话</h1>
        <p className="mt-1 text-sm text-neutral-500">一个会话 = 一台一次性测试机：起容器、构建应用、全程录屏。</p>
      </header>

      <section className="flex flex-wrap items-end gap-3 rounded-lg border border-neutral-800 bg-neutral-900/40 p-4">
        <label className="text-sm">
          <span className="mb-1 block text-neutral-400">应用</span>
          <input
            value={app}
            onChange={(e) => setApp(e.target.value)}
            className="w-40 rounded-md border border-neutral-700 bg-neutral-950 px-3 py-1.5 text-sm outline-none focus:border-neutral-500"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-neutral-400">分支 / commit</span>
          <input
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            className="w-56 rounded-md border border-neutral-700 bg-neutral-950 px-3 py-1.5 text-sm outline-none focus:border-neutral-500"
          />
        </label>
        <button
          onClick={onCreate}
          disabled={busy}
          className="rounded-md bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white transition hover:bg-emerald-500 disabled:opacity-50"
        >
          {busy ? '创建中…' : '新建会话'}
        </button>
        {error && <span className="text-sm text-red-400">{error}</span>}
      </section>

      <section className="overflow-hidden rounded-lg border border-neutral-800">
        <table className="w-full text-sm">
          <thead className="bg-neutral-900/60 text-left text-neutral-400">
            <tr>
              <th className="px-4 py-2 font-medium">会话</th>
              <th className="px-4 py-2 font-medium">应用 / 版本</th>
              <th className="px-4 py-2 font-medium">状态</th>
              <th className="px-4 py-2 font-medium">结论</th>
              <th className="px-4 py-2 font-medium">创建</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {sessions.length === 0 && (
              <tr>
                <td colSpan={6} className="px-4 py-10 text-center text-neutral-500">
                  还没有会话。上面填个分支点「新建会话」就能起一台。
                </td>
              </tr>
            )}
            {sessions.map((s) => (
              <tr key={s.id} className="border-t border-neutral-800 hover:bg-neutral-900/40">
                <td className="px-4 py-2">
                  <a href={`#/sessions/${s.id}`} className="font-mono text-xs text-sky-400 hover:underline">
                    {s.id}
                  </a>
                </td>
                <td className="px-4 py-2">
                  {s.app}
                  <span className="ml-2 font-mono text-xs text-neutral-500">{s.ref || '—'}</span>
                </td>
                <td className="px-4 py-2">
                  <span className={`rounded-md border px-2 py-0.5 text-xs ${STATUS_STYLE[s.status] ?? ''}`}>
                    {s.status}
                  </span>
                </td>
                <td className={`px-4 py-2 ${VERDICT_STYLE[s.verdict ?? ''] ?? 'text-neutral-500'}`}>
                  {s.verdict || '—'}
                </td>
                <td className="px-4 py-2 text-neutral-400">{formatAgo(s.createdAt)}</td>
                <td className="px-4 py-2 text-right">
                  {!['finished', 'failed', 'abandoned'].includes(s.status) && (
                    <button onClick={() => onStop(s.id)} className="text-xs text-neutral-400 hover:text-red-400">
                      结束
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </div>
  );
}
