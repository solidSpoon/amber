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
 * 屏幕取景框：录的就是这台电脑的整块屏幕，这里也原样整块显示。
 *
 * 为什么不裁：窗口在哪、旁边有没有别的窗口、桌面上有没有弹出的对话框，
 * 都是「这是一台真电脑」的证据。按窗口矩形裁掉一圈桌面，等于把证据裁掉了。
 * 所以这里只按屏幕的宽高比占位，不做任何缩放或位移。
 */
export function ScreenStage({
  display,
  children,
  className,
}: {
  display?: Geometry | null;
  children: ReactNode;
  className?: string;
}) {
  const full = display && display.width > 0 && display.height > 0 ? display : null;

  return (
    <div
      className={cn(
        'relative overflow-hidden rounded-md border bg-neutral-950',
        !full && 'aspect-video w-full',
        className,
      )}
      style={full ? { aspectRatio: `${full.width} / ${full.height}` } : undefined}
    >
      {children}
    </div>
  );
}
