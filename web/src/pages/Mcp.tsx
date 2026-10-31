import { useEffect, useMemo, useState } from 'react';
import { toast } from 'sonner';
import { CopyIcon } from 'lucide-react';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { PageHeader } from '@/components/page-header';

interface ToolInfo {
  name: string;
  description: string;
  required?: string[];
}

interface McpInfo {
  httpEnabled: boolean;
  httpUrl: string;
  stdio: { command: string[]; env: Record<string, string> };
  tools: ToolInfo[];
}

type Client = 'claude' | 'cursor' | 'vscode' | 'cli';

const CLIENTS: { key: Client; label: string }[] = [
  { key: 'claude', label: 'Claude Desktop' },
  { key: 'cursor', label: 'Cursor' },
  { key: 'vscode', label: 'VS Code' },
  { key: 'cli', label: '命令行' },
];

/**
 * MCP 配置页：把「怎么连、能调什么」直接摆出来，配好就能用。
 *
 * 页面上的工具清单来自后端 tools/list（不是手写文档），
 * 所以只要服务里的工具变了，这里跟着变，不会出现"文档说有、实际调不到"。
 */
export default function Mcp() {
  const [info, setInfo] = useState<McpInfo | null>(null);
  const [error, setError] = useState('');
  const [client, setClient] = useState<Client>('claude');

  useEffect(() => {
    fetch('/api/mcp')
      .then((resp) => (resp.ok ? resp.json() : Promise.reject(new Error(`HTTP ${resp.status}`))))
      .then((data: McpInfo) => setInfo(data))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  const snippet = useMemo(() => (info ? buildSnippet(client, info) : ''), [client, info]);

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle>读取 MCP 配置失败</AlertTitle>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    );
  }

  if (!info) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  return (
    <div className="flex max-w-4xl flex-col gap-6">
      <PageHeader
        title="MCP 接入"
        description="把一台机器交给任意支持 MCP 的 AI：模型和 key 留在你自己的客户端里，这里只提供机器与工具。"
      />

      <Card>
        <CardHeader>
          <CardTitle>连接地址</CardTitle>
          <CardDescription>内网直连即可，不需要令牌；别把它暴露到公网。</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <Row label="HTTP 端点" value={info.httpUrl} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>客户端配置</CardTitle>
          <CardDescription>复制下面的片段到对应客户端的配置里，重启客户端生效。</CardDescription>
        </CardHeader>
        <CardContent>
          <Tabs value={client} onValueChange={(value) => setClient(String(value) as Client)}>
            <TabsList>
              {CLIENTS.map((item) => (
                <TabsTrigger key={item.key} value={item.key}>
                  {item.label}
                </TabsTrigger>
              ))}
            </TabsList>
            {CLIENTS.map((item) => (
              <TabsContent key={item.key} value={item.key}>
                <pre className="overflow-x-auto rounded-md bg-neutral-950 p-3 text-xs leading-relaxed text-neutral-100">
                  {snippet}
                </pre>
              </TabsContent>
            ))}
          </Tabs>
          <div className="mt-2 flex items-center justify-between gap-3 text-xs text-muted-foreground">
            <span>{hintFor(client)}</span>
            <CopyButton text={snippet} label="复制配置" />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>提供的工具（{info.tools.length}）</CardTitle>
          <CardDescription>清单来自服务自身 tools/list，不是手写文档。</CardDescription>
        </CardHeader>
        <CardContent>
          {/* 手机：三列表格放不下（说明被挤成一列字，必填参数整列在屏幕外），改成一条工具一张卡 */}
          <div className="flex flex-col gap-2 md:hidden">
            {info.tools.map((tool) => (
              <div key={tool.name} className="flex flex-col gap-1 rounded-lg border p-3">
                <code className="font-mono text-xs font-medium">{tool.name}</code>
                <p className="text-xs leading-relaxed text-muted-foreground">{tool.description}</p>
                <span className="font-mono text-[11px] text-muted-foreground">
                  必填：{(tool.required ?? []).join(', ') || '—'}
                </span>
              </div>
            ))}
          </div>

          <div className="hidden md:block">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>工具</TableHead>
                  {/* 说明是全表最长的一列，允许换行，否则会把"必填参数"挤出卡片 */}
                  <TableHead className="whitespace-normal">说明</TableHead>
                  <TableHead>必填参数</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {info.tools.map((tool) => (
                  <TableRow key={tool.name} className="align-top">
                    <TableCell className="font-mono text-xs whitespace-nowrap">{tool.name}</TableCell>
                    <TableCell className="whitespace-normal text-muted-foreground">{tool.description}</TableCell>
                    <TableCell className="font-mono text-xs whitespace-nowrap text-muted-foreground">
                      {(tool.required ?? []).join(', ') || '—'}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Alert>
        <AlertTitle>注意</AlertTitle>
        <AlertDescription>
          <ul className="list-disc space-y-1 pl-4">
            <li>令牌能起容器、能操作被测应用，等同于这台机器的操作权：不要发群里或写进仓库。</li>
            <li>控制台本身没有登录态，只应在可信局域网内访问；对外暴露请走反向代理 + 认证。</li>
            <li>
              同一台机器同时最多跑 <code className="font-mono">RIGD_MAX_SESSIONS</code>（默认 2）个会话，超出直接拒绝。
            </li>
            <li>
              会话闲置超过 <code className="font-mono">RIGD_IDLE_TIMEOUT</code>（默认 10 分钟）会被自动回收并归档每批的片段。
            </li>
          </ul>
        </AlertDescription>
      </Alert>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="w-20 shrink-0 text-xs text-muted-foreground">{label}</span>
      <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1 font-mono text-xs">{value}</code>
      <CopyButton text={value} label="复制" />
    </div>
  );
}

/**
 * 复制按钮：非安全上下文（http + 非 localhost）下浏览器不给剪贴板权限，
 * 这种情况明确弹错让人手动选中，而不是假装复制成功。
 */
function CopyButton({ text, label }: { text: string; label: string }) {
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success('已复制到剪贴板');
    } catch {
      toast.error('复制失败', { description: '当前页面不是安全上下文，请手动选中复制。' });
    }
  }
  return (
    <Button variant="ghost" size="xs" onClick={copy}>
      <CopyIcon data-icon="inline-start" />
      {label}
    </Button>
  );
}

function hintFor(client: Client): string {
  switch (client) {
    case 'claude':
      return '写入 Claude Desktop 配置后重启客户端';
    case 'cursor':
      return '放到项目下 .cursor/mcp.json（或 ~/.cursor/mcp.json 做全局）';
    case 'vscode':
      return '放到工作区 .vscode/mcp.json';
    default:
      return '在同机终端里直接跑；管道模式下它就是一个 MCP server 进程';
  }
}

/** 按客户端生成配置片段：远程用 HTTP + 令牌，同机也可以用 stdio 命令 */
function buildSnippet(client: Client, info: McpInfo): string {
  const stdio = {
    command: info.stdio.command[0],
    args: info.stdio.command.slice(1),
    env: info.stdio.env,
  };
  if (client === 'cli') {
    const envs = Object.entries(stdio.env)
      .map(([k, v]) => `${k}=${v}`)
      .join(' \\\n  ');
    return `${envs} \\\n  ${[stdio.command, ...stdio.args].join(' ')}`;
  }
  const server = { url: info.httpUrl };
  if (client === 'vscode') {
    return JSON.stringify({ servers: { rigd: { type: 'http', ...server } } }, null, 2);
  }
  return JSON.stringify({ mcpServers: { rigd: server } }, null, 2);
}
