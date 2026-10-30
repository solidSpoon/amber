import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { PlusIcon, SquareIcon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Spinner } from '@/components/ui/spinner';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { PageHeader } from '@/components/page-header';
import { StatusBadge, VerdictBadge } from '@/components/session-badges';
import { api, formatAgo, type AppInfo, type Session } from '../api';

/**
 * 会话列表：新建、查看、结束。
 *
 * 应用下拉的选项来自后端注册表（/api/apps），前端不写死任何应用名——
 * 这个控制台服务的是"任意 GUI 应用"，不是某一个项目。
 */
export default function Sessions() {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [apps, setApps] = useState<AppInfo[]>([]);
  const [app, setApp] = useState('');
  const [ref, setRef] = useState('main');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const data = await api.listSessions();
      setSessions(data.sessions ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    api
      .apps()
      .then((data) => {
        setApps(data.apps ?? []);
        // 默认选第一个可跑的应用，省掉"每次都要先点一下下拉"
        setApp((current) => current || data.apps?.[0]?.key || '');
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  // 列表用 3 秒轮询：控制台只需要"接近实时"，轮询比长连接少一堆边界情况
  // （重连、页面隐藏、代理超时），也让无头浏览器能直接截到首屏。
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function onCreate() {
    setBusy(true);
    setError('');
    try {
      const created = await api.createSession(app, ref.trim());
      toast.success('会话已创建，正在准备机器', { description: created.id });
      await load();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setError(message);
      toast.error('创建会话失败', { description: message });
    } finally {
      setBusy(false);
    }
  }

  async function onStop(id: string) {
    try {
      await api.stopSession(id, 'inconclusive', '管理台手动结束');
      toast.success('已请求结束会话', { description: id });
      await load();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setError(message);
      toast.error('结束会话失败', { description: message });
    }
  }

  const selected = apps.find((a) => a.key === app);

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="验收会话" description="一个会话 = 一台一次性测试机：起容器、构建应用、全程录屏。" />

      <Card>
        <CardHeader>
          <CardTitle>新建会话</CardTitle>
          <CardDescription>指定应用与分支/提交即可；机器数量受并发上限限制。</CardDescription>
        </CardHeader>
        <CardContent>
          <FieldGroup className="grid gap-4 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto] sm:items-end">
            <Field>
              <FieldLabel htmlFor="app">应用</FieldLabel>
              <Select
                value={app}
                onValueChange={(value) => setApp(String(value))}
                items={apps.map((item) => ({ value: item.key, label: item.key }))}
              >
                <SelectTrigger id="app" className="w-full">
                  <SelectValue placeholder="选择应用" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {apps.map((item) => (
                      <SelectItem key={item.key} value={item.key}>
                        {item.key}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FieldDescription>{selected?.description || selected?.repo || '来自服务端应用注册表'}</FieldDescription>
            </Field>

            <Field>
              <FieldLabel htmlFor="ref">分支 / 标签 / 提交</FieldLabel>
              <Input id="ref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="main" />
              <FieldDescription>容器里按这个 ref 拉代码，与宿主当前工作区无关。</FieldDescription>
            </Field>

            <Field className="sm:pb-6">
              <Button onClick={onCreate} disabled={busy || !app || !ref.trim()}>
                {busy ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}
                {busy ? '创建中…' : '新建会话'}
              </Button>
            </Field>
          </FieldGroup>
        </CardContent>
      </Card>

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
                <EmptyDescription>上面选个应用、填个分支，点「新建会话」就能起一台。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>会话</TableHead>
                  <TableHead>应用 / 版本</TableHead>
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
                      <TableCell>
                        {s.app}
                        <span className="ml-2 font-mono text-xs text-muted-foreground">{s.ref || '—'}</span>
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
                          <Button variant="ghost" size="xs" onClick={() => void onStop(s.id)}>
                            <SquareIcon data-icon="inline-start" />
                            结束
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
