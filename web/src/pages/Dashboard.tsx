import { useEffect, useRef, useState } from 'react';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import { api, formatBytes, machineName, type Machine, type RuntimeInfo, type Sample } from '../api';

/**
 * 首页看板：每台候选机器的实时屏幕 + 宿主资源 + 最近会话。
 *
 * 「机器」是固定槽位（M1/M2，数量由并发上限决定），没有会话的槽位就是关机态。
 * 这是刻意的拟物：看板上应该一眼看出"几台机器开着、屏幕上在放什么"，
 * 而不是去读一张会话表格。这里不假设被测应用是什么，应用名一律来自会话数据。
 */
export default function Dashboard() {
  const [machines, setMachines] = useState<Machine[]>([]);
  const [samples, setSamples] = useState<Sample[]>([]);
  const [info, setInfo] = useState<RuntimeInfo | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    async function load() {
      try {
        const [m, s, meta] = await Promise.all([api.machines(), api.metrics(), api.info()]);
        if (!alive) return;
        setMachines(m.machines ?? []);
        setSamples(s.samples ?? []);
        setInfo(meta);
        setError('');
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      }
    }
    void load();
    // 看板用轮询：数值是"看一眼"的语义，2 秒一次足够，也避免长连接带来的各种边界情况
    const timer = window.setInterval(load, 2000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, []);

  const latest = samples.at(-1);

  return (
    <div className="mx-auto flex w-full max-w-[1680px] flex-col gap-12 px-2 sm:px-6 lg:px-10">
      {/* 第一屏：只有机器。
          不放标题、不放说明——看板要回答的就一句"现在台上是什么"，
          一屏两台机器，往下翻才是数字和流水。 */}
      {/* 高度按"视口高度减去外面几层 padding"算：main 的 p-4/6/8 + 这里的 py。
          算准了，第一屏底部就不会漏出下一节的边。 */}
      {/* 手机上不撑满一屏：两台机器是上下堆着的，"一屏两台"的版式在手机上只会变成大片空白。
          窄屏改成从顶部排起，资源数字紧跟其后。 */}
      <section className="flex flex-col justify-center gap-8 py-2 max-sm:min-h-0 max-sm:justify-start max-sm:gap-6 sm:min-h-[calc(100svh-2rem)] sm:gap-10 sm:py-6 md:min-h-[calc(100svh-3rem)] lg:min-h-[calc(100svh-4rem)] lg:py-10">
        {error && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="text-sm text-destructive">读取看板数据失败：{error}</CardContent>
          </Card>
        )}
        <div className="grid items-start gap-10 md:grid-cols-2 md:gap-10 lg:gap-14">
          {machines.length === 0 && !error && (
            <>
              <MonitorSkeleton />
              <MonitorSkeleton />
            </>
          )}
          {machines.map((m) => (
            <MachineCard key={m.slot} machine={m} />
          ))}
        </div>
      </section>

      {/* 往下翻：资源与流水 */}
      {/* 不用分割线分屏：线会把第一屏"切开"，而且滚动时那根线会卡在视口边缘。
          两屏的关系靠留白和滚动本身表达：一屏是"台上"，一屏是"账本"。 */}
      <section className="grid gap-4 pt-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="宿主 CPU"
          value={latest ? `${latest.cpuPct.toFixed(0)}%` : '—'}
          hint={latest ? `最近采样 ${new Date(latest.at).toLocaleTimeString('zh-CN', { hour12: false })}` : undefined}
        />
        <Stat
          label="内存"
          value={latest ? pct(latest.memUsed, latest.memTotal) : '—'}
          hint={latest ? `${formatBytes(latest.memUsed)} / ${formatBytes(latest.memTotal)}` : undefined}
        />
        <Stat
          label="磁盘"
          value={latest ? pct(latest.diskUsed, latest.diskTotal) : '—'}
          hint={latest ? `${formatBytes(latest.diskUsed)} / ${formatBytes(latest.diskTotal)}` : undefined}
        />
        <Stat
          label="运行中的机器"
          value={latest ? String(latest.containers) : '—'}
          hint={info ? `并发上限 ${info.maxSessions} · 屏幕 ${info.display.width}×${info.display.height}` : undefined}
        />
      </section>

    </div>
  );
}

/** 已运行时长：看板读的是"开了多久"，不是"几分钟前" */
function formatUptime(since: string): string {
  const ms = Date.now() - new Date(since).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '—';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return '不到 1 分钟';
  if (mins < 60) return `${mins} 分钟`;
  const hours = Math.floor(mins / 60);
  return `${hours} 小时 ${mins % 60} 分`;
}

function pct(used: number, total: number): string {
  if (!total) return '0%';
  return `${((used / total) * 100).toFixed(0)}%`;
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card>
      <CardHeader>
        <CardDescription>{label}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {hint && <CardContent className="text-xs text-muted-foreground">{hint}</CardContent>}
    </Card>
  );
}

/** 看板首屏的空骨架：比转圈更早给出"这里会出现两台机器"的形状 */
function MonitorSkeleton() {
  return (
    <div className="flex flex-col items-center gap-3">
      <Skeleton className="aspect-video w-full rounded-[16px]" />
      <Skeleton className="h-4 w-40" />
    </div>
  );
}

/**
 * 一台机器：外壳（边框/支架/阴影）+ 屏幕（实时画面或关机态）。
 *
 * 拟物要克制：只做"这块是屏幕"的暗示（内凹、圆角、轻微反光），
 * 不加花哨装饰，否则在浅色界面里会显脏。
 *
 * 外壳的金属色**写死**，不走主题 token：这里是 Pro Display XDR 那种阳极氧化铝，
 * 屏幕内部是黑的，都是"物理属性"。品牌色是琥珀，机器是铝，两者混在一起就不像一台真机器了。
 */
function MachineCard({ machine }: { machine: Machine }) {
  const online = machine.occupied && (machine.status === 'ready' || machine.status === 'starting');
  const screenRef = useRef<HTMLImageElement>(null);
  const [streamFailed, setStreamFailed] = useState(false);

  // 每次切换会话都重建 <img src>，避免沿用上一个会话的帧
  useEffect(() => {
    setStreamFailed(false);
  }, [machine.sessionId]);

  const screenSrc = machine.sessionId ? `/api/sessions/${machine.sessionId}/screen.mjpg` : '';

  return (
    // 尺寸只由一件事决定：**一屏要同时装下两台机器和它们的铭牌**。
    // 所以不按栏宽撑满，而是三条上限取最小：列宽、40rem、以及「视口高度减掉铭牌与留白」
    // 反算出来的宽度（乘 1.17 是机身图的宽高比）。撑满栏宽的后果是机身把铭牌顶出首屏，
    // 看板第一眼只能看到两张大图，看不到“这台机器在干什么”。
    <article
      // 手机上两台是上下堆着的：机身收到 74%，两台和铭牌才装得下一屏。
      // 宽度上限只能写在 inline style 里（要和 svh 一起算），所以手机那一档用 CSS 变量传进去。
      className="mx-auto flex w-full flex-col items-center max-sm:[--cap:74%]"
      style={{ maxWidth: 'min(var(--cap, 100%), 40rem, calc((100svh - 12rem) * 1.17))' }}
    >
      {/* 机身用 Apple 官方那张设备渲染图当外框（public/img/frame.png）。
          那张图本身是"透明屏"素材：屏幕区域是全透明的洞，周围的机器是不透明的，
          所以内容是**垫在下面**、透过洞露出来的，不是贴在图上。

          屏幕矩形是量出来的：原图 1912×1633（现用的 frame.png 是 1600×1367，同比例）里，
          屏在 x 46→1873、y 46→1073。换成百分比写在下面，换一张分辨率不同的渲染图时只需要重算这四个数。 */}
      <div className="relative w-full" style={{ aspectRatio: '1912 / 1633' }}>
        {/* 内容比洞大 3px 的四周：外框内沿有一圈抗锯齿的半透明像素，
            正好贴上会漏出一条白边，压进去一点就没了（多出来的部分被黑边框盖住）。 */}
        <div
          className="absolute overflow-hidden bg-black"
          style={{
            left: 'calc(2.197% - 3px)',
            top: 'calc(2.572% - 3px)',
            width: 'calc(95.607% + 6px)',
            height: 'calc(62.952% + 6px)',
          }}
        >
          {online && screenSrc && !streamFailed ? (
            <img
              ref={screenRef}
              src={screenSrc}
              alt={`${machineName(machine.slot)} 实时画面`}
              // contain 而不是 cover：分辨率会变，cover 会按框裁掉两侧，应用自己的侧边栏就没了
              className="size-full object-contain"
              onError={() => setStreamFailed(true)}
            />
          ) : (
            <LockScreen machine={machine} />
          )}

          {/* 屏幕反光：一条很淡的斜向高光，让"这是块玻璃"成立但不抢内容 */}
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-white/10 via-transparent to-transparent" />

          {online && (
            <div className="absolute top-2 left-2 flex items-center gap-2 rounded-full bg-neutral-950/60 px-2.5 py-1 text-[10px] font-medium text-neutral-50 backdrop-blur">
              <span className="relative flex size-1.5">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-75" />
                <span className="relative inline-flex size-1.5 rounded-full bg-success" />
              </span>
              LIVE
            </div>
          )}
        </div>

        {/* 外框压在内容上面；不接鼠标事件，免得挡住里面本来就有的交互 */}
        <img
          src="/img/frame.png"
          alt=""
          aria-hidden
          draggable={false}
          className="pointer-events-none absolute inset-0 size-full select-none"
        />
      </div>

      {/* 铭牌：
          高度写死不好，但参差更难看——在线的时候这里是三行（编号+徽章 / 这次会话 / CPU 内存 已运行），
          空闲只有两行。空着的那行用 min-h 占住，两台机器的铭牌才能上下对齐。
          话往上看：铭牌一长，早先底部对齐的布局就会把有会话那台的机身顶高一截。 */}
      <div className="mt-2 flex min-h-[4.5rem] w-full flex-col gap-0.5 px-1 max-sm:min-h-0">
        <div className="flex items-baseline gap-2">
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-semibold">{machineName(machine.slot)}</span>
            {online ? (
              <Badge variant="success">{machine.status === 'starting' ? '启动中' : (machine.app ?? '占用中')}</Badge>
            ) : machine.occupied && machine.status === 'starting' ? (
              <Badge variant="warning">启动中</Badge>
            ) : (
              <Badge variant="outline">空闲</Badge>
            )}
          </div>
        </div>
        {/* 铭牌上写「这台机器此刻在干什么」，不写会话 ID：ID 是内部编号，
            人看的是这台机器被谁占着、在做什么。单独一行，避免被挤到卡片另一头。 */}
        {online && machine.sessionId ? (
          <a
            href={`#/sessions/${machine.sessionId}`}
            className="max-w-full truncate text-xs text-foreground/80 hover:text-foreground hover:underline"
          >
            {machine.title || machine.app || '这次会话'} →
          </a>
        ) : (
          // 占位：空闲时没有会话链接，但那一行的高度要留着，否则铭牌一短、两列就错位
          <span className="h-4" aria-hidden />
        )}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          {online ? (
            <>
              {machine.ref && <span className="font-mono">{machine.ref}</span>}
              {machine.statsOk ? (
                <>
                  <span>CPU {machine.stats.CPUPct.toFixed(0)}%</span>
                  <span>内存 {formatBytes(machine.stats.MemUsed)}</span>
                </>
              ) : (
                <span>统计中…</span>
              )}
              {machine.since && <span>已运行 {formatUptime(machine.since)}</span>}
            </>
          ) : (
            <span>空闲槽位，随时可以接单</span>
          )}
        </div>
      </div>
    </article>
  );
}

/**
 * 空闲态：macOS 锁屏。
 *
 * 为什么不画黑屏：黑屏只说明"没信号"，锁屏说明"这台机器在，只是没人用"。
 * 看板上两台机器大部分时间都是空的，一整块黑反而像坏了；锁屏既安静又带信息（时间、机器名）。
 *
 * 壁纸不用图片，用三层柔光斑 + 深底堆出来——单文件二进制里不想再多一张几百 K 的 JPEG。
 * 字号用容器查询单位（cqw），所以屏幕缩到多小，锁屏元素都按比例缩，不会溢出。
 */
function LockScreen({ machine }: { machine: Machine }) {
  const starting = machine.occupied && machine.status === 'starting';
  const now = useClock();

  const hh = String(now.getHours()).padStart(2, '0');
  const mm = String(now.getMinutes()).padStart(2, '0');
  const week = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'][now.getDay()];
  const date = `${now.getMonth() + 1}月${now.getDate()}日 ${week}`;

  return (
    <div className="@container relative size-full overflow-hidden bg-[#0b1524]">
      {/* 壁纸：四团柔光叠成一层"流动的"渐变（桃 / 青 / 紫 / 玫），跟新版 macOS 的默认壁纸同一路数。
          纯 CSS 叠出来，不放图片：单文件二进制里不想多几百 K，而且缩放不糊。
          外面套一层 blur 是为了把渐变之间的过渡抹平，不然能看见硬边。 */}
      <div className="absolute inset-0 bg-[#140b1e]" />
      <div
        className="absolute -inset-[12%] blur-[38px] saturate-[1.05]"
        style={{
          background: [
            'radial-gradient(38% 44% at 16% 14%, #ff9d6c 0%, rgba(255,157,108,0) 58%)',
            'radial-gradient(42% 46% at 78% 20%, #63d8dd 0%, rgba(99,216,221,0) 60%)',
            'radial-gradient(46% 50% at 66% 82%, #a469e6 0%, rgba(164,105,230,0) 62%)',
            'radial-gradient(40% 44% at 26% 74%, #f0568c 0%, rgba(240,86,140,0) 58%)',
            'radial-gradient(60% 60% at 50% 50%, rgba(24,14,34,0.55) 0%, rgba(24,14,34,0) 70%)',
          ].join(','),
        }}
      />
      <div className="absolute inset-0 bg-gradient-to-b from-black/25 via-transparent to-black/35" />

      <div className="relative flex size-full flex-col items-center justify-center text-white">
        <div className="text-[2.1cqw] font-medium text-white/80 [text-shadow:0_1px_3px_rgba(0,0,0,0.45)]">
          {date}
        </div>
        <div className="mt-[0.6cqw] text-[10.5cqw] leading-none font-extralight tracking-[-0.03em] tabular-nums [text-shadow:0_2px_14px_rgba(0,0,0,0.45)]">
          {hh}:{mm}
        </div>

        <div className="mt-[3.2cqw] flex items-center gap-[1.4cqw] rounded-full bg-white/10 py-[0.9cqw] pr-[2.4cqw] pl-[0.9cqw] ring-1 ring-white/15 backdrop-blur-md">
          <div className="grid size-[4.4cqw] place-items-center rounded-full bg-white/20 text-[1.9cqw] font-semibold text-white/90">
            {machineName(machine.slot).slice(0, 1)}
          </div>
          <div className="flex flex-col items-start leading-tight">
            <span className="text-[2cqw] font-medium">{machineName(machine.slot)}</span>
            <span className="text-[1.5cqw] text-white/65">
              {starting ? '正在启动…' : '空闲，等待接单'}
            </span>
          </div>
        </div>
      </div>

      {/* 玻璃反光：一条很淡的斜向高光，和在线时的反射保持一致 */}
      <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-white/10 via-transparent to-transparent" />
    </div>
  );
}

/** 锁屏上的时钟：一分钟跳一次就够，不用每帧渲染 */
function useClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 20_000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}
