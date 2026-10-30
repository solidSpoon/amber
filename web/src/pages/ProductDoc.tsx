import { useEffect, useState } from 'react';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Skeleton } from '@/components/ui/skeleton';

/**
 * 产品文档页：把 docs/PRODUCT.md 原样渲染出来（标题也来自文档本身）。
 *
 * 文档只有一份——页面读的是仓库里那份文件，不在这里再写一遍，
 * 否则"页面上说的"和"仓库里写的"迟早对不上。
 */
export default function ProductDoc() {
  const [text, setText] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    fetch('/api/docs/product')
      .then((resp) => (resp.ok ? resp.text() : Promise.reject(new Error(`HTTP ${resp.status}`))))
      .then(setText)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle>读取产品文档失败</AlertTitle>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    );
  }

  if (!text) {
    return (
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  return (
    <article className="mx-auto flex w-full max-w-3xl flex-col gap-6 pb-16">
      <div className="flex flex-col gap-4 text-sm leading-relaxed">
        <Markdown
          remarkPlugins={[remarkGfm]}
          components={{
            h1: ({ children }) => <h1 className="mt-2 text-2xl font-semibold tracking-tight">{children}</h1>,
            h2: ({ children }) => (
              <h2 className="mt-8 border-b pb-2 text-lg font-semibold tracking-tight">{children}</h2>
            ),
            h3: ({ children }) => <h3 className="mt-6 text-base font-semibold">{children}</h3>,
            p: ({ children }) => <p className="leading-relaxed">{children}</p>,
            ul: ({ children }) => <ul className="flex list-disc flex-col gap-1.5 pl-5">{children}</ul>,
            ol: ({ children }) => <ol className="flex list-decimal flex-col gap-1.5 pl-5">{children}</ol>,
            li: ({ children }) => <li className="leading-relaxed">{children}</li>,
            strong: ({ children }) => <strong className="font-semibold text-foreground">{children}</strong>,
            blockquote: ({ children }) => (
              <blockquote className="border-l-2 pl-4 text-muted-foreground">{children}</blockquote>
            ),
            hr: () => <hr className="my-2 border-border" />,
            a: ({ href, children }) => (
              <a href={href} className="underline underline-offset-2" target="_blank" rel="noreferrer">
                {children}
              </a>
            ),
            table: ({ children }) => (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-xs">{children}</table>
              </div>
            ),
            th: ({ children }) => (
              <th className="border-b px-2 py-1.5 text-left font-medium text-muted-foreground">{children}</th>
            ),
            td: ({ children }) => <td className="border-b px-2 py-1.5 align-top">{children}</td>,
            code: ({ children }) => <code className="rounded bg-muted px-1 py-0.5 font-mono text-[12px]">{children}</code>,
          }}
        >
          {text}
        </Markdown>
      </div>
    </article>
  );
}
