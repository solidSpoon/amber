import { useCallback, useEffect, useState } from 'react';
import { api, formatAgo, type Session } from '../api';

const STATUS_STYLE: Record<string, string> = {
  ready: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  starting: 'bg-sky-50 text-sky-700 border-sky-200',
  stopping: 'bg-amber-50 text-amber-700 border-amber-200',
  finished: 'bg-neutral-100 text-neutral-600 border-neutral-200',
  failed: 'bg-red-50 text-red-700 border-red-200',
  abandoned: 'bg-purple-50 text-purple-700 border-purple-200',
};

const VERDICT_STYLE: Record<string, string> = {
  pass: 'text-emerald-600',
  fail: 'text-red-600',
  inconclusive: 'text-amber-600',
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

  // 列表用 3 秒轮询：控制台只需要"接近实时"，轮询比长连接少一堆边界情况
  // （重连、页面隐藏、代理超时），也让无头浏览器能直接截到首屏。
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
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

      <section className="flex flex-wrap items-end gap-3 rounded-lg border border-neutral-200 bg-white p-4">
        <label className="text-sm">
          <span className="mb-1 block text-neutral-500">应用</span>
          <input
            value={app}
            onChange={(e) => setApp(e.target.value)}
            className="w-40 rounded-md border border-neutral-300 bg-white px-3 py-1.5 text-sm outline-none focus:border-neutral-400"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-neutral-500">分支 / commit</span>
          <input
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            className="w-56 rounded-md border border-neutral-300 bg-white px-3 py-1.5 text-sm outline-none focus:border-neutral-400"
          />
        </label>
        <button
          onClick={onCreate}
          disabled={busy}
          className="rounded-md bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white transition hover:bg-emerald-500 disabled:opacity-50"
        >
          {busy ? '创建中…' : '新建会话'}
        </button>
        {error && <span className="text-sm text-red-600">{error}</span>}
      </section>

      <section className="overflow-hidden rounded-lg border border-neutral-200 bg-white">
        <table className="w-full text-sm">
          <thead className="bg-neutral-50 text-left text-neutral-500">
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
                <td colSpan={6} className="px-4 py-10 text-center text-neutral-400">
                  还没有会话。上面填个分支点「新建会话」就能起一台。
                </td>
              </tr>
            )}
            {sessions.map((s) => (
              <tr key={s.id} className="border-t border-neutral-200 hover:bg-neutral-50">
                <td className="px-4 py-2">
                  <a href={`#/sessions/${s.id}`} className="font-mono text-xs text-sky-600 hover:underline">
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
                <td className="px-4 py-2 text-neutral-500">{formatAgo(s.createdAt)}</td>
                <td className="px-4 py-2 text-right">
                  {!['finished', 'failed', 'abandoned'].includes(s.status) && (
                    <button onClick={() => onStop(s.id)} className="text-xs text-neutral-500 hover:text-red-600">
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
