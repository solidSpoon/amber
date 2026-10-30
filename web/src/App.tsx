import { useEffect, useState } from 'react';
import { BookOpenIcon, ListChecksIcon, MonitorPlayIcon, PlugIcon } from 'lucide-react';
import { buttonVariants } from '@/components/ui/button';
import { Toaster } from '@/components/ui/sonner';
import { cn } from '@/lib/utils';
import Dashboard from './pages/Dashboard';
import Mcp from './pages/Mcp';
import ProductDoc from './pages/ProductDoc';
import Sessions from './pages/Sessions';
import SessionDetail from './pages/SessionDetail';

// 用 hash 路由而不是 react-router：页面只有三个，且部署在单文件二进制里，
// hash 不需要服务端配合，刷新/直链都不会 404。
type Route =
  | { name: 'dashboard' }
  | { name: 'sessions' }
  | { name: 'session'; id: string }
  | { name: 'mcp' }
  | { name: 'docs' };

function parseRoute(hash: string): Route {
  const clean = hash.replace(/^#\/?/, '');
  if (clean.startsWith('sessions/')) return { name: 'session', id: clean.slice('sessions/'.length) };
  if (clean === 'sessions') return { name: 'sessions' };
  if (clean === 'mcp') return { name: 'mcp' };
  if (clean === 'docs') return { name: 'docs' };
  return { name: 'dashboard' };
}

const NAV = [
  { href: '#/', label: '看板', key: 'dashboard', icon: MonitorPlayIcon },
  { href: '#/sessions', label: '验收会话', key: 'sessions', icon: ListChecksIcon },
  { href: '#/mcp', label: 'MCP 接入', key: 'mcp', icon: PlugIcon },
  // 产品文档排在最后：它是"依据"，不是日常操作入口
  { href: '#/docs', label: '产品文档', key: 'docs', icon: BookOpenIcon },
] as const;

export default function App() {
  const [route, setRoute] = useState<Route>(parseRoute(window.location.hash));

  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);

  const links = NAV.map((item) => {
    const active = route.name === item.key || (route.name === 'session' && item.key === 'sessions');
    const Icon = item.icon;
    return (
      // 导航是链接不是按钮：用链接元素 + buttonVariants 借样式，键盘和右键新开标签都正常
      <a
        key={item.href}
        href={item.href}
        aria-current={active ? 'page' : undefined}
        className={cn(
          buttonVariants({ variant: 'ghost', size: 'sm' }),
          'justify-start',
          active ? 'bg-muted text-foreground' : 'text-muted-foreground',
        )}
      >
        <Icon data-icon="inline-start" />
        {item.label}
      </a>
    );
  });

  return (
    // 外壳固定一屏高、内部各自滚动：滚动正文时侧边栏必须钉住，
    // 否则导航会跟着列表一起走远（长列表下尤其难受）。
    // 窄屏（手机/竖屏窗口）把侧边栏收成顶部一条横向导航，否则正文只剩一百多像素。
    <div className="flex h-svh flex-col overflow-hidden bg-background text-foreground md:flex-row">
      <aside className="hidden h-full w-60 shrink-0 flex-col gap-6 overflow-y-auto border-r bg-sidebar p-4 md:flex">
        <div className="flex flex-col gap-0.5 px-2 pt-1">
          <span className="text-base font-semibold tracking-tight">rigd</span>
          <span className="text-xs text-muted-foreground">一次性 GUI 验收控制台</span>
        </div>
        <nav className="flex flex-col gap-1">{links}</nav>
      </aside>

      <nav className="flex shrink-0 items-center gap-1 overflow-x-auto border-b bg-sidebar px-3 py-2 md:hidden">
        <span className="mr-2 shrink-0 text-sm font-semibold tracking-tight">rigd</span>
        {links}
      </nav>

      <main className="min-h-0 min-w-0 flex-1 overflow-y-auto p-4 md:p-6 lg:p-8">
        {route.name === 'dashboard' && <Dashboard />}
        {route.name === 'sessions' && <Sessions />}
        {route.name === 'session' && <SessionDetail id={route.id} />}
        {route.name === 'mcp' && <Mcp />}
        {route.name === 'docs' && <ProductDoc />}
      </main>

      <Toaster position="bottom-right" />
    </div>
  );
}
