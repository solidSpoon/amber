import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { Trash2Icon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Skeleton } from '@/components/ui/skeleton';
import { PageHeader } from '@/components/page-header';
import { api, formatAgo, formatBytes, type StorageUsage } from '../api';

/**
 * 存储页：画面占了多大地方，以及把画面整体收走。
 *
 * 为什么单独一页：记录很小（文字 + 时间戳），占地方的全是画面——每个会话一份母片、
 * 一批一条片段、失败步骤和 capture 的整屏截图。母片是有意留着的（AI 事后要重新取帧、
 * 核对某一秒到底发生了什么），所以得有个地方能看见它、也能一次性收走。
 *
 * 有意只做"全部清空"，不做逐条删除：删掉某一个会话的画面，记录还在、画面没了，
 * 读的人只会更糊涂；要省空间就整批收，这也是唯一能让数字真的降下来的操作。
 */
export default function Storage() {
  const [usage, setUsage] = useState<StorageUsage | null>(null);
  const [error, setError] = useState('');
  // 二次确认：清空是不可逆的，一次点击就执行太容易误触
  const [confirming, setConfirming] = useState(false);
  const [purging, setPurging] = useState(false);

  const load = useCallback(async () => {
    try {
      setUsage(await api.storage());
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onPurge() {
    setPurging(true);
    try {
      const result = await api.purgeMedia();
      toast.success('已清空画面', {
        description: `释放 ${formatBytes(result.freedBytes)}，涉及 ${result.sessions} 个会话` +
          (result.skipped > 0 ? `；${result.skipped} 个还在跑，已跳过` : ''),
      });
      setConfirming(false);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setPurging(false);
    }
  }

  const totals = usage?.totals;
  const hasMedia = (totals?.totalBytes ?? 0) > 0;

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-5">
      <PageHeader
        title="存储"
        description="录像、片段、截图各占多少。清空只收走画面，会话和步骤记录（含每步的时间戳）保留。"
      />

      {error && (
        <Alert variant="destructive">
          <AlertTitle>读取存储信息失败</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="整场录像" value={totals ? formatBytes(totals.videoBytes) : '—'} hint="每个会话一份，可被 AI 重新取帧" />
        <Stat label="片段" value={totals ? formatBytes(totals.clipBytes) : '—'} hint="每批几秒，记录页直接播这个" />
        <Stat label="截图" value={totals ? formatBytes(totals.shotBytes) : '—'} hint="失败步骤与 capture 的整屏图" />
        <Stat
          label="产物库"
          value={usage ? formatBytes(usage.blobs.bytes) : '—'}
          hint={usage ? `${usage.blobs.count} 份字节，按保留期自动回收` : undefined}
        />
      </section>

      <Card>
        <CardHeader>
          <CardTitle>画面占用</CardTitle>
          <CardDescription>
            {usage
              ? `合计 ${formatBytes(totals?.totalBytes ?? 0)}，${totals?.sessions ?? 0} 个会话。`
              : '读取中…'}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {usage === null ? (
            <div className="flex flex-col gap-3">
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
            </div>
          ) : usage.sessions.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>还没有会话</EmptyTitle>
                <EmptyDescription>AI 用完一次机器之后，它的录像和截图会出现在这里。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className="flex flex-col divide-y text-sm">
              <div className="flex items-center gap-3 pb-2 text-xs text-muted-foreground">
                <span className="min-w-0 flex-1">会话</span>
                <span className="w-20 text-right">录像</span>
                <span className="w-20 text-right">片段</span>
                <span className="w-20 text-right">截图</span>
                <span className="w-20 text-right">合计</span>
              </div>
              {usage.sessions.map((s) => (
                <div key={s.id} className="flex items-center gap-3 py-2">
                  <div className="min-w-0 flex-1">
                    <a href={`#/sessions/${s.id}`} className="block truncate hover:underline">
                      {s.title || s.id}
                      {s.running && <span className="ml-2 text-xs text-muted-foreground">运行中</span>}
                    </a>
                    <div className="truncate font-mono text-[11px] text-muted-foreground">
                      {s.id}
                      {s.endedAt ? ` · ${formatAgo(s.endedAt)}` : ''}
                    </div>
                  </div>
                  <span className="w-20 text-right tabular-nums">{sizeCell(s.videoBytes)}</span>
                  <span className="w-20 text-right tabular-nums">{sizeCell(s.clipBytes)}</span>
                  <span className="w-20 text-right tabular-nums">{sizeCell(s.shotBytes)}</span>
                  <span className="w-20 text-right font-medium tabular-nums">{sizeCell(s.totalBytes)}</span>
                </div>
              ))}
            </div>
          )}

          <div className="flex flex-wrap items-center gap-3 border-t pt-4">
            {confirming ? (
              <>
                <Button variant="destructive" size="sm" onClick={() => void onPurge()} disabled={purging}>
                  <Trash2Icon data-icon="inline-start" />
                  {purging ? '正在清空…' : '确认清空全部画面'}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={purging}>
                  取消
                </Button>
                <span className="text-xs text-muted-foreground">
                  会删掉录像、片段、截图；会话、步骤文字和时间戳保留，但记录里的画面将打不开。
                </span>
              </>
            ) : (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setConfirming(true)}
                  disabled={!hasMedia || purging}
                >
                  <Trash2Icon data-icon="inline-start" />
                  清空全部画面
                </Button>
                <span className="text-xs text-muted-foreground">
                  不可撤销。运行中的会话会跳过；产物库不在这里清（它按保留期自动回收）。
                </span>
              </>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
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

/** 0 字节写成一条横线：一列 0 B 比空着更吵 */
function sizeCell(bytes: number): string {
  return bytes > 0 ? formatBytes(bytes) : '—';
}
