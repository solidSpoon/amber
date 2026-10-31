import { useEffect, useState } from 'react';
import { BookOpenIcon, ListChecksIcon, MessageSquareTextIcon, MonitorPlayIcon, PlugIcon } from 'lucide-react';
import { Toaster } from '@/components/ui/sonner';
import { cn } from '@/lib/utils';
import Dashboard from './pages/Dashboard';
import FeedbackPage from './pages/Feedback';
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
  | { name: 'feedback' }
  | { name: 'mcp' }
  | { name: 'docs' };

function parseRoute(hash: string): Route {
  const clean = hash.replace(/^#\/?/, '');
  if (clean.startsWith('sessions/')) return { name: 'session', id: clean.slice('sessions/'.length) };
  if (clean === 'sessions') return { name: 'sessions' };
  if (clean === 'feedback') return { name: 'feedback' };
  if (clean === 'mcp') return { name: 'mcp' };
  if (clean === 'docs') return { name: 'docs' };
  return { name: 'dashboard' };
}

const NAV = [
  { href: '#/', label: '看板', key: 'dashboard', icon: MonitorPlayIcon },
  { href: '#/sessions', label: '会话', key: 'sessions', icon: ListChecksIcon },
  { href: '#/feedback', label: '反馈', key: 'feedback', icon: MessageSquareTextIcon },
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
        // 不再借 buttonVariants：它的 padding 会跟这里的 px-3.5 打架（同特异性比的是输出顺序，
        // 谁在后谁赢，结果导航项的图标缩进变成 6px）。导航项样式自己写，反而干净。
        className={cn(
          // shrink-0 + nowrap：窄屏顶部导航是横向滚动的，允许收缩的话中文会被挤成竖排
          'flex h-10 shrink-0 items-center gap-2.5 rounded-md px-3.5 text-[0.8125rem] font-medium whitespace-nowrap transition-colors',
          'outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50',
          // 选中态必须把 hover 也写死，否则鼠标一放上去底色会被 hover 覆盖，
          // 看起来像"选中丢了"——这正是之前那个怪象
          active
            ? 'bg-primary/12 text-primary hover:bg-primary/18 hover:text-primary'
            : 'text-muted-foreground hover:bg-muted/70 hover:text-foreground',
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
        {/* px-3.5 和下面的导航项对齐：导航项的图标缩进也是 3.5，这样 logo 的图形
            和导航图标在同一条竖线上，不然 logo 会往左顶出去半个字 */}
        <div className="flex items-center gap-2.5 px-3.5 pt-1">
          {/* 品牌图形直接用图标那一份，页面和 Dock 里是同一块琥珀 */}
          <img src="/amber.svg" alt="" width={28} height={28} className="size-7 shrink-0" />
          <span className="flex flex-col gap-0.5">
            <span className="text-base font-semibold tracking-tight">Amber</span>
            <span className="text-xs text-muted-foreground">给 AI 的一次性机器</span>
          </span>
        </div>
        <nav className="flex flex-col gap-1">{links}</nav>
      </aside>

      {/* 手机上的导航：品牌只留图标（文字太占地方，五个标签挤不下），标签横排、超宽横向滚动。
          外面的 main 是独立滚动区，所以这条不会跟着正文走。 */}
      <div className="relative shrink-0 border-b bg-sidebar md:hidden">
        <nav className="flex items-center gap-1 overflow-x-auto px-2 py-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          <span className="mr-1 flex shrink-0 items-center">
            <img src="/amber.svg" alt="Amber" width={22} height={22} className="size-[22px]" />
          </span>
          {links}
        </nav>
        {/* 右边那一道渐变是"还能往右滑"的提示：手机上五个标签放不下，没有它看着像被裁掉了 */}
        <div className="pointer-events-none absolute inset-y-0 right-0 w-8 bg-gradient-to-l from-sidebar to-transparent" />
      </div>

      <main className="min-h-0 min-w-0 flex-1 overflow-y-auto p-3 sm:p-4 md:p-6 lg:p-8">
        {route.name === 'dashboard' && <Dashboard />}
        {route.name === 'sessions' && <Sessions />}
        {route.name === 'session' && <SessionDetail id={route.id} />}
        {route.name === 'feedback' && <FeedbackPage />}
        {route.name === 'mcp' && <Mcp />}
        {route.name === 'docs' && <ProductDoc />}
      </main>

      <Toaster position="bottom-right" />
    </div>
  );
}
