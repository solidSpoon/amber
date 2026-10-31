import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { SquareIcon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { PageHeader } from '@/components/page-header';
import { StatusBadge } from '@/components/session-badges';
import { api, formatAgo, type Session } from '../api';

/** 产物来源的短标签：手机上一行只放得下"哪一个产物"，不是整条路径 */
function payloadShort(payload?: string): string {
  const raw = payload ?? '';
  if (raw.startsWith('dir:')) {
    const parts = raw.slice(4).replace(/\/+$/, '').split('/').filter(Boolean);
    return parts.slice(-2).join('/') || raw;
  }
  return raw.replace(/^manifest:sha256:/, '') || '—';
}

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
      await api.stopSession(id);
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
        title="会话"
        description="会话由 AI 通过 MCP 创建，这一页只看结果。一个会话 = 一台一次性机器。"
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
                <EmptyDescription>让 AI 通过 MCP 调 amber_start 起一台；跑完这里会出现它的记录。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <>
              {/* 手机：六列表格在 390px 上只能横向滚，改成一条会话一张卡——
                  手机上没有“扫一眼表格”这件事，只有“往下翻着看”。 */}
              <div className="flex flex-col gap-2 md:hidden">
                {sessions.map((s) => {
                  const running = !['finished', 'failed', 'abandoned'].includes(s.status);
                  return (
                    <div key={s.id} className="flex flex-col gap-1.5 rounded-lg border p-3">
                      <div className="flex items-start justify-between gap-3">
                        <a href={`#/sessions/${s.id}`} className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">{s.title || s.app}</div>
                          <div className="truncate font-mono text-[11px] text-muted-foreground">{s.id}</div>
                        </a>
                        <StatusBadge status={s.status} />
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                        <span>{s.app}</span>
                        <span>{formatAgo(s.createdAt)}</span>
                        {running && (
                          <Button variant="ghost" size="xs" onClick={() => void onStop(s.id)} title="AI 卡死或断连时用">
                            <SquareIcon data-icon="inline-start" />
                            强制回收
                          </Button>
                        )}
                      </div>
                      {/* 手机上长路径只剩开头几个字符，等于没写：这里只留末尾两段，
                          完整来源放到 title 里，需要时点开看 */}
                      <div className="truncate font-mono text-[11px] text-muted-foreground" title={s.payload}>
                        {payloadShort(s.payload)}
                        {s.ref ? ` · ${s.ref}` : ''}
                      </div>
                    </div>
                  );
                })}
              </div>

              <div className="hidden md:block">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>会话</TableHead>
                      <TableHead>产物 / 版本</TableHead>
                      <TableHead>状态</TableHead>
                      <TableHead>创建</TableHead>
                      <TableHead className="text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {sessions.map((s) => {
                      const running = !['finished', 'failed', 'abandoned'].includes(s.status);
                      return (
                        <TableRow key={s.id}>
                          <TableCell className="max-w-[26rem]">
                            {/* 标题是这次验收的意图：读者按它认人，会话 ID 退到第二行 */}
                            <a href={`#/sessions/${s.id}`} className="block hover:underline">
                              <div className="truncate text-sm font-medium">{s.title || s.app}</div>
                              <div className="truncate font-mono text-[11px] text-muted-foreground">{s.id}</div>
                            </a>
                          </TableCell>
                          <TableCell className="max-w-[22rem]">
                            <div className="truncate">{s.app}</div>
                            <div className="truncate font-mono text-[11px] text-muted-foreground">
                              {(s.payload || '—').replace(/^manifest:sha256:/, '')}
                              {s.ref ? ` · ${s.ref}` : ''}
                            </div>
                          </TableCell>
                          <TableCell>
                            <StatusBadge status={s.status} />
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">{formatAgo(s.createdAt)}</TableCell>
                          <TableCell className="text-right">
                            {running && (
                              <Button
                                variant="ghost"
                                size="xs"
                                onClick={() => void onStop(s.id)}
                                title="AI 卡死或断连时用"
                              >
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
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
