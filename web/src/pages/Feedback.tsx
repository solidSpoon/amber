import { useEffect, useState } from 'react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty';
import { Skeleton } from '@/components/ui/skeleton';
import { PageHeader } from '@/components/page-header';
import { api, formatAgo, type Feedback } from '../api';

/**
 * 反馈页：AI 用完这套流程之后留下的意见。
 *
 * 单开一页而不是塞在看板里，是因为它跟"机器现在跑得怎么样"没有关系：
 * 看板回答"现在开着几台、上面在放什么"，这里回答"这套东西哪里该改"。
 * 两件事混在一个页面里，读的人会把它当成运行状态的一部分。
 *
 * 它也不进任何一条会话的记录——记录只讲那次会话发生了什么。
 */
export default function FeedbackPage() {
  const [items, setItems] = useState<Feedback[] | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    async function load() {
      try {
        const res = await api.feedback();
        if (!alive) return;
        setItems(res.feedback ?? []);
        setError('');
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      }
    }
    void load();
    return () => {
      alive = false;
    };
  }, []);

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-5">
      <PageHeader
        title="反馈"
        description="AI 用完这套流程后留下的意见，用来决定产品下一步改什么。它不是任何一次会话的记录。"
      />

      {error && (
        <Alert variant="destructive">
          <AlertTitle>读取反馈失败</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>全部反馈</CardTitle>
          <CardDescription>
            {items === null ? '读取中…' : `${items.length} 条，按时间倒序。`}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {items === null ? (
            <div className="flex flex-col gap-3">
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
            </div>
          ) : items.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>还没有反馈</EmptyTitle>
                <EmptyDescription>AI 觉得哪里别扭时，会通过 amber_feedback 写在这里。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className="flex flex-col divide-y">
              {items.map((f) => (
                <article key={f.id} className="flex flex-col gap-1.5 py-3 first:pt-0 last:pb-0">
                  <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                    <span>{formatAgo(f.at)}</span>
                    {f.rating > 0 && <span>· 自评 {f.rating}/5</span>}
                    {f.sessionId && (
                      <a href={`#/sessions/${f.sessionId}`} className="font-mono hover:underline">
                        · 来自 {f.sessionId}
                      </a>
                    )}
                  </div>
                  <p className="text-sm leading-relaxed whitespace-pre-wrap">{f.body}</p>
                </article>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
