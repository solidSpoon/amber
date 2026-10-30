// 与后端 DTO 一一对应的类型 + 极薄的请求封装。
// 刻意不引入状态库：控制台只有三个页面，useEffect + 显式刷新足够，少一层就少一处不同步。

export type SessionStatus = 'starting' | 'ready' | 'stopping' | 'finished' | 'failed' | 'abandoned';

export interface Session {
  id: string;
  app: string;
  ref: string;
  commitSha?: string;
  status: SessionStatus;
  cdpPort: number;
  vncPort: number;
  createdAt: string;
  startedAt?: string;
  endedAt?: string;
  idleDeadline?: string;
  hasVideo: boolean;
  videoDurationMs: number;
  verdict?: string;
  summary?: string;
  error?: string;
}

export interface SessionEvent {
  seq: number;
  at: string;
  tool: string;
  args: unknown;
  ok: boolean;
  durationMs: number;
  videoOffsetMs: number;
  result: string;
  artifact?: string;
}

/** 看板上的一台机器（一个固定槽位） */
export interface Machine {
  slot: number;
  occupied: boolean;
  sessionId?: string;
  app?: string;
  ref?: string;
  status?: SessionStatus;
  since?: string;
  idleUntil?: string;
  cdpPort?: number;
  vncPort?: number;
  stats: { CPUPct: number; MemUsed: number; MemLimit: number };
  statsOk: boolean;
}

export interface Sample {
  at: string;
  cpuPct: number;
  memUsed: number;
  memTotal: number;
  diskUsed: number;
  diskTotal: number;
  containers: number;
}

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(path);
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.error ?? `请求失败：${resp.status}`);
  }
  return (await resp.json()) as T;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const raw = await resp.json().catch(() => ({}));
    throw new Error(raw.error ?? `请求失败：${resp.status}`);
  }
  return (await resp.json()) as T;
}

export const api = {
  listSessions: () => get<{ sessions: Session[] }>('/api/sessions'),
  getSession: (id: string) => get<Session>(`/api/sessions/${id}`),
  createSession: (app: string, ref: string) => post<Session>('/api/sessions', { app, ref }),
  stopSession: (id: string, verdict?: string, summary?: string) =>
    post<Session>(`/api/sessions/${id}/stop`, { verdict, summary }),
  listEvents: (id: string) => get<{ events: SessionEvent[] }>(`/api/sessions/${id}/events`),
  metrics: () => get<{ samples: Sample[] }>('/api/system/metrics'),
  machines: () => get<{ machines: Machine[] }>('/api/machines'),
};

/** 订阅 SSE；返回取消函数，组件卸载时调用。 */
export function subscribe<T>(path: string, onData: (payload: T) => void): () => void {
  const source = new EventSource(path);
  source.onmessage = (event) => {
    try {
      onData(JSON.parse(event.data) as T);
    } catch {
      // 单帧解析失败不应该打断整条流
    }
  };
  return () => source.close();
}

/** 机器槽位的显示名：M1 / M2 ……依次类推 */
export function machineName(slot: number): string {
  return `M${slot + 1}`;
}

export function formatBytes(bytes: number): string {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatAgo(iso?: string): string {
  if (!iso) return '—';
  const diff = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(diff)) return '—';
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return '刚刚';
  if (mins < 60) return `${mins} 分钟前`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours} 小时前`;
  return `${Math.floor(hours / 24)} 天前`;
}
