import type { ReactNode } from 'react';

/**
 * 页面标题区：标题 + 一句话说明 + 右侧操作。
 *
 * 单独抽出来是因为四个页面的标题排版必须一致——分页各写一份，
 * 早晚会出现字号/间距跑偏，而这种不一致在浅色界面上特别刺眼。
 */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-end justify-between gap-4">
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </header>
  );
}
