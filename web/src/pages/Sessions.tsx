import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { SquareIcon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { PageHeader } from '@/components/page-header';
import { StatusBadge, VerdictBadge } from '@/components/session-badges';
import { api, formatAgo, type Session } from '../api';

/**
 * 会话列表：只看，不建。
 *
 * 这个控制台是给**人看结果**的，不是给人操作的：会话由外部的 AI 通过 MCP 起，
 * 界面负责回答"现在有哪几台、验的是什么、结论是什么"。所以这里刻意没有"新建会话"表单——
 * 一旦提供，就会有人问"那我在页面上点一下不行吗"，然后两条操作路径就开始漂移。
 *
 * 唯一保留的人工动作是「强制回收」：AI 卡死或连接断了的时候，总得有个地方把机器收回去。
 */
export default function Sessions() {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const data = await api.listSessions();
      setSessions(data.sessions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  // 列表用 3 秒轮询：控制台只需要"接近实时"，轮询比长连接少一堆边界情况
  // （重连、页面隐藏、代理超时），也让无头浏览器能直接截到首屏。
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function onStop(id: string) {
    try {
      await api.stopSession(id, 'inconclusive', '控制台强制回收');
      toast.success('已请求回收', { description: id });
      await load();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setError(message);
      toast.error('回收失败', { description: message });
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="验收会话"
        description="会话由 AI 通过 MCP 创建；这一页只看结果。一个会话 = 一台一次性测试机。"
      />

      {error && (
        <Alert variant="destructive">
          <AlertTitle>操作失败</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>全部会话</CardTitle>
          <CardDescription>{sessions.length} 条，按创建时间倒序。</CardDescription>
        </CardHeader>
        <CardContent>
          {sessions.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>还没有会话</EmptyTitle>
                <EmptyDescription>让 AI 通过 MCP 调 start_session 起一台；跑完这里会出现它的报告。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>会话</TableHead>
                  <TableHead>产物 / 版本</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>结论</TableHead>
                  <TableHead>创建</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.map((s) => {
                  const running = !['finished', 'failed', 'abandoned'].includes(s.status);
                  return (
                    <TableRow key={s.id}>
                      <TableCell>
                        <a href={`#/sessions/${s.id}`} className="font-mono text-xs hover:underline">
                          {s.id}
                        </a>
                      </TableCell>
                      <TableCell className="max-w-[24rem]">
                        <div className="truncate">{s.app}</div>
                        <div className="truncate font-mono text-[11px] text-muted-foreground">
                          {(s.payload || '—').replace(/^manifest:sha256:/, '')}
                          {s.ref ? ` · ${s.ref}` : ''}
                        </div>
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={s.status} />
                      </TableCell>
                      <TableCell>
                        <VerdictBadge verdict={s.verdict} />
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{formatAgo(s.createdAt)}</TableCell>
                      <TableCell className="text-right">
                        {running && (
                          <Button variant="ghost" size="xs" onClick={() => void onStop(s.id)} title="AI 卡死或断连时用">
                            <SquareIcon data-icon="inline-start" />
                            强制回收
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
