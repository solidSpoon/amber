import { Badge } from '@/components/ui/badge';
import type { SessionStatus } from '../api';

// 状态到徽章外观的映射。这里只挑语义变体，不写颜色类，
// 换主题（甚至以后加深色）时不用回来改。
const STATUS: Record<SessionStatus, { label: string; variant: 'default' | 'secondary' | 'outline' | 'destructive' | 'success' | 'warning' }> = {
  starting: { label: '启动中', variant: 'secondary' },
  ready: { label: '就绪', variant: 'success' },
  stopping: { label: '收尾中', variant: 'warning' },
  finished: { label: '已结束', variant: 'outline' },
  failed: { label: '失败', variant: 'destructive' },
  abandoned: { label: '已放弃', variant: 'outline' },
};

/** 会话状态徽章：英文状态码是接口契约，这里翻成人话显示 */
export function StatusBadge({ status }: { status: SessionStatus }) {
  const spec = STATUS[status] ?? { label: status, variant: 'outline' as const };
  return <Badge variant={spec.variant}>{spec.label}</Badge>;
}
