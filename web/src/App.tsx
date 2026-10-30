import { useEffect, useState } from 'react';
import { ListChecksIcon, MonitorPlayIcon, PlugIcon } from 'lucide-react';
import { buttonVariants } from '@/components/ui/button';
import { Toaster } from '@/components/ui/sonner';
import { cn } from '@/lib/utils';
import Dashboard from './pages/Dashboard';
import Mcp from './pages/Mcp';
import Sessions from './pages/Sessions';
import SessionDetail from './pages/SessionDetail';

// 用 hash 路由而不是 react-router：页面只有三个，且部署在单文件二进制里，
// hash 不需要服务端配合，刷新/直链都不会 404。
type Route =
  | { name: 'dashboard' }
  | { name: 'sessions' }
  | { name: 'session'; id: string }
  | { name: 'mcp' };

function parseRoute(hash: string): Route {
  const clean = hash.replace(/^#\/?/, '');
  if (clean.startsWith('sessions/')) return { name: 'session', id: clean.slice('sessions/'.length) };
  if (clean === 'sessions') return { name: 'sessions' };
  if (clean === 'mcp') return { name: 'mcp' };
  return { name: 'dashboard' };
}

const NAV = [
  { href: '#/', label: '看板', key: 'dashboard', icon: MonitorPlayIcon },
  { href: '#/sessions', label: '验收会话', key: 'sessions', icon: ListChecksIcon },
  { href: '#/mcp', label: 'MCP 接入', key: 'mcp', icon: PlugIcon },
] as const;

export default function App() {
  const [route, setRoute] = useState<Route>(parseRoute(window.location.hash));

  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);

  return (
    <div className="flex min-h-full bg-background text-foreground">
      <aside className="flex w-60 shrink-0 flex-col gap-6 border-r bg-sidebar p-4">
        <div className="flex flex-col gap-0.5 px-2 pt-1">
          <span className="text-base font-semibold tracking-tight">rigd</span>
          <span className="text-xs text-muted-foreground">一次性 GUI 验收控制台</span>
        </div>
        <nav className="flex flex-col gap-1">
          {NAV.map((item) => {
            const active = route.name === item.key || (route.name === 'session' && item.key === 'sessions');
            const Icon = item.icon;
            return (
              // 导航是链接不是按钮：用链接元素 + buttonVariants 借样式，键盘和右键新开标签都正常
              <a
                key={item.href}
                href={item.href}
                aria-current={active ? 'page' : undefined}
                className={cn(
                  buttonVariants({ variant: 'ghost' }),
                  'justify-start',
                  active ? 'bg-muted text-foreground' : 'text-muted-foreground',
                )}
              >
                <Icon data-icon="inline-start" />
                {item.label}
              </a>
            );
          })}
        </nav>
      </aside>

      <main className="min-w-0 flex-1 p-6 lg:p-8">
        {route.name === 'dashboard' && <Dashboard />}
        {route.name === 'sessions' && <Sessions />}
        {route.name === 'session' && <SessionDetail id={route.id} />}
        {route.name === 'mcp' && <Mcp />}
      </main>

      <Toaster position="bottom-right" />
    </div>
  );
}
