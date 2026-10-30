import { useEffect, useMemo, useState } from 'react';

interface ToolInfo {
  name: string;
  description: string;
  required?: string[];
}

interface McpInfo {
  httpEnabled: boolean;
  httpUrl: string;
  token: string;
  stdio: { command: string[]; env: Record<string, string> };
  tools: ToolInfo[];
}

type Client = 'claude' | 'cursor' | 'vscode' | 'cli';

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
  const [reveal, setReveal] = useState(false);

  useEffect(() => {
    fetch('/api/mcp')
      .then((resp) => (resp.ok ? resp.json() : Promise.reject(new Error(`HTTP ${resp.status}`))))
      .then((data: McpInfo) => setInfo(data))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  const snippet = useMemo(() => (info ? buildSnippet(client, info) : ''), [client, info]);

  if (error) return <p className="text-sm text-red-600">读取 MCP 配置失败：{error}</p>;
  if (!info) return <p className="text-sm text-neutral-400">加载中…</p>;

  const masked = reveal ? info.token : `${info.token.slice(0, 8)}${'•'.repeat(20)}`;

  return (
    <div className="max-w-4xl space-y-8">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-900">MCP 接入</h1>
        <p className="mt-1 text-sm text-neutral-500">
          把验收能力交给任意支持 MCP 的 AI：模型和 key 留在你自己的客户端里，这里只提供机器与工具。
        </p>
      </header>

      <section className="space-y-3 rounded-xl border border-neutral-200 bg-white p-5">
        <h2 className="text-sm font-medium text-neutral-900">连接地址</h2>
        <Row label="HTTP 端点" value={info.httpUrl} />
        <div className="flex items-center gap-2">
          <span className="w-24 shrink-0 text-xs text-neutral-500">访问令牌</span>
          <code className="min-w-0 flex-1 truncate rounded-md bg-neutral-50 px-2 py-1 font-mono text-xs text-neutral-700">
            {masked}
          </code>
          <button onClick={() => setReveal(!reveal)} className="text-xs text-neutral-500 hover:text-neutral-900">
            {reveal ? '隐藏' : '显示'}
          </button>
          <CopyButton text={info.token} />
        </div>
        <p className="pt-1 text-xs text-neutral-400">
          本机（127.0.0.1）直连不需要令牌；局域网/跨机器必须带 <code className="font-mono">Authorization: Bearer &lt;令牌&gt;</code>。
        </p>
      </section>

      <section className="space-y-3 rounded-xl border border-neutral-200 bg-white p-5">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-medium text-neutral-900">客户端配置</h2>
          <div className="flex gap-1 rounded-lg bg-neutral-100 p-1">
            {(
              [
                ['claude', 'Claude Desktop'],
                ['cursor', 'Cursor'],
                ['vscode', 'VS Code'],
                ['cli', '命令行'],
              ] as [Client, string][]
            ).map(([key, label]) => (
              <button
                key={key}
                onClick={() => setClient(key)}
                className={`rounded-md px-2.5 py-1 text-xs transition ${
                  client === key ? 'bg-white text-neutral-900 shadow-sm' : 'text-neutral-500 hover:text-neutral-800'
                }`}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
        <pre className="overflow-x-auto rounded-lg bg-neutral-950 p-3 text-xs leading-relaxed text-neutral-100">
          {snippet}
        </pre>
        <div className="flex items-center justify-between text-xs text-neutral-400">
          <span>{hintFor(client)}</span>
          <CopyButton text={snippet} />
        </div>
      </section>

      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <h2 className="text-sm font-medium text-neutral-900">提供的工具（{info.tools.length}）</h2>
          <span className="text-xs text-neutral-400">清单来自服务自身 tools/list</span>
        </div>
        <div className="overflow-hidden rounded-xl border border-neutral-200 bg-white">
          <table className="w-full text-sm">
            <thead className="bg-neutral-50 text-left text-xs text-neutral-500">
              <tr>
                <th className="px-4 py-2 font-medium">工具</th>
                <th className="px-4 py-2 font-medium">说明</th>
                <th className="px-4 py-2 font-medium">必填参数</th>
              </tr>
            </thead>
            <tbody>
              {info.tools.map((tool) => (
                <tr key={tool.name} className="border-t border-neutral-100 align-top">
                  <td className="whitespace-nowrap px-4 py-2 font-mono text-xs text-neutral-900">{tool.name}</td>
                  <td className="px-4 py-2 text-neutral-600">{tool.description}</td>
                  <td className="whitespace-nowrap px-4 py-2 font-mono text-xs text-neutral-400">
                    {(tool.required ?? []).join(', ') || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="rounded-xl border border-amber-200 bg-amber-50 p-4 text-xs leading-relaxed text-amber-800">
        <div className="mb-1 font-medium">注意</div>
        <ul className="list-disc space-y-1 pl-4">
          <li>令牌能起容器、能操作被测应用，等同于这台机器的操作权：不要把它发到群里或写进仓库。</li>
          <li>控制台本身没有登录态，只应在可信局域网内访问；对外暴露请走反向代理 + 认证。</li>
          <li>同一台机器同时最多跑 <code className="font-mono">RIGD_MAX_SESSIONS</code>（默认 2）个会话，超出会直接拒绝。</li>
          <li>会话闲置超过 <code className="font-mono">RIGD_IDLE_TIMEOUT</code>（默认 10 分钟）会被自动回收并归档录像。</li>
        </ul>
      </section>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="w-24 shrink-0 text-xs text-neutral-500">{label}</span>
      <code className="min-w-0 flex-1 truncate rounded-md bg-neutral-50 px-2 py-1 font-mono text-xs text-neutral-700">
        {value}
      </code>
      <CopyButton text={value} />
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      window.setTimeout(() => setDone(false), 1500);
    } catch {
      // 非安全上下文（http + 非 localhost）下剪贴板不可用，交给用户手动选中
      setDone(false);
    }
  }
  return (
    <button onClick={copy} className="shrink-0 text-xs text-neutral-500 hover:text-neutral-900">
      {done ? '已复制' : '复制'}
    </button>
  );
}

function hintFor(client: Client): string {
  switch (client) {
    case 'claude':
      return '写入 ~/Library/Application Support/Claude/claude_desktop_config.json 后重启 Claude';
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
  const server = {
    url: info.httpUrl,
    headers: { Authorization: `Bearer ${info.token}` },
  };
  if (client === 'vscode') {
    return JSON.stringify({ servers: { rigd: { type: 'http', ...server } } }, null, 2);
  }
  return JSON.stringify({ mcpServers: { rigd: server } }, null, 2);
}
