import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

/** 画面几何（像素）：x/y 是左上角，width/height 是有内容的尺寸 */
export interface Geometry {
  x: number;
  y: number;
  width: number;
  height: number;
}

/**
 * 屏幕取景框：录像/预览/截图都从这里出画面。
 *
 * 为什么需要它：录屏抓的是整块虚拟屏（这样弹窗、菜单也录得进去），而应用窗口
 * 不一定铺满它。直接 <video /> 铺开就会出现「角落里一个小窗 + 一大片空桌面」。
 * 这里用 frame（有内容的区域）+ display（整幅像素）把画面裁到窗口那部分：
 * 放大 display/frame 倍，再把窗口左上角挪到框的左上角 —— 纯渲染，不改录像本身。
 *
 * frame 与 display 相同（正常情况）时就是恒等变换，不会多一次缩放。
 */
export function ScreenStage({
  frame,
  display,
  children,
  className,
}: {
  frame?: Geometry | null;
  display?: Geometry | null;
  children: ReactNode;
  className?: string;
}) {
  const view = frame && frame.width > 0 && frame.height > 0 ? frame : display;
  const full = display && display.width > 0 ? display : view;
  if (!view || !full) {
    return <div className={cn('aspect-video w-full rounded-md bg-neutral-950', className)}>{children}</div>;
  }

  const scaleX = (full.width / view.width) * 100;
  const scaleY = (full.height / view.height) * 100;

  return (
    <div
      className={cn('relative overflow-hidden rounded-md border bg-neutral-950', className)}
      style={{ aspectRatio: `${view.width} / ${view.height}` }}
    >
      <div
        className="absolute"
        style={{
          width: `${scaleX}%`,
          height: `${scaleY}%`,
          left: `${(-view.x / view.width) * 100}%`,
          top: `${(-view.y / view.height) * 100}%`,
        }}
      >
        {children}
      </div>
    </div>
  );
}
