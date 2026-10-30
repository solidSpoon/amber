import { useEffect, useState } from 'react';
import Dashboard from './pages/Dashboard';
import Sessions from './pages/Sessions';
import SessionDetail from './pages/SessionDetail';

// 用 hash 路由而不是 react-router：页面只有三个，且部署在单文件二进制里，
// hash 不需要服务端配合，刷新/直链都不会 404。
type Route = { name: 'dashboard' } | { name: 'sessions' } | { name: 'session'; id: string };

function parseRoute(hash: string): Route {
  const clean = hash.replace(/^#\/?/, '');
  if (clean.startsWith('sessions/')) return { name: 'session', id: clean.slice('sessions/'.length) };
  if (clean === 'sessions') return { name: 'sessions' };
  return { name: 'dashboard' };
}

const NAV: { href: string; label: string; key: Route['name'] }[] = [
  { href: '#/', label: '看板', key: 'dashboard' },
  { href: '#/sessions', label: '验收会话', key: 'sessions' },
];

export default function App() {
  const [route, setRoute] = useState<Route>(parseRoute(window.location.hash));

  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);

  return (
    <div className="flex min-h-full">
      <aside className="w-56 shrink-0 border-r border-neutral-200 bg-white p-4">
        <div className="mb-6">
          <div className="text-lg font-semibold tracking-tight">rigd</div>
          <div className="text-xs text-neutral-500">一次性 GUI 验收控制台</div>
        </div>
        <nav className="space-y-1">
          {NAV.map((item) => (
            <a
              key={item.href}
              href={item.href}
              className={`block rounded-md px-3 py-2 text-sm transition ${
                route.name === item.key
                  ? 'bg-neutral-100 font-medium text-neutral-900'
                  : 'text-neutral-600 hover:bg-neutral-100 hover:text-neutral-900'
              }`}
            >
              {item.label}
            </a>
          ))}
        </nav>
      </aside>

      <main className="min-w-0 flex-1 p-6">
        {route.name === 'dashboard' && <Dashboard />}
        {route.name === 'sessions' && <Sessions />}
        {route.name === 'session' && <SessionDetail id={route.id} />}
      </main>
    </div>
  );
}
