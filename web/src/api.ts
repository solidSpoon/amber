// 与后端 DTO 一一对应的类型 + 极薄的请求封装。
// 刻意不引入状态库：控制台只有三个页面，useEffect + 显式刷新足够，少一层就少一处不同步。

export type SessionStatus = 'starting' | 'ready' | 'stopping' | 'finished' | 'failed' | 'abandoned';

/** 画面几何（像素）；后端只在几何已知时才给，旧录像会是 undefined */
export interface Geometry {
  x: number;
  y: number;
  width: number;
  height: number;
}

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
  /** 这次验的是哪份产物、挂了哪些素材槽位 */
  payload?: string;
  assets?: Record<string, string>;
  /** 录像里真正有内容的那块区域（= 应用窗口） */
  frame?: Geometry;
  /** 录像的整幅像素（= 录制时的虚拟屏） */
  display?: Geometry;
  verdict?: string;
  summary?: string;
  error?: string;
}

export interface LogSource {
  name: string;
  label: string;
  bytes: number;
}

export type EventKind = 'action' | 'note';

export interface SessionEvent {
  /** 这一段连续操作的起止（毫秒，相对录像起点）；同一批的几步共用一条片段 */
  batchStartMs: number;
  batchEndMs: number;
  seq: number;
  at: string;
  tool: string;
  args: unknown;
  ok: boolean;
  /** 这一步的性质：做了什么 / 断言了什么 */
  kind: EventKind;
  /** 人话描述，后端按动作 + 元素名自动生成 */
  caption: string;
  /** 为什么做这一步（调用方给，可空） */
  intent?: string;
  /** 期望看到什么（调用方给，可空） */
  expect?: string;
  durationMs: number;
  videoOffsetMs: number;
  result: string;
  artifact?: string;
  /** 这一步的画面，如 shots/150405.123-click.jpg */
  shot?: string;
  /** 这一步当时窗口的矩形（窗口中途变过，裁剪得按当时算） */
  frame?: Geometry;
  /** 有没有可点的 4 秒短片 */
  clip: boolean;
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

/** 已上传过的产物（内容寻址库里的一棵目录树） */
export interface PayloadInfo {
  digest: string;
  bytes: number;
  files: number;
  refs: number;
  usedAt: string;
}

export interface PayloadsResponse {
  payloads: PayloadInfo[];
  storage: { blobs: number; bytes: number };
  uploadLimitMB: number;
  roots: string[];
}

/** 运行时信息：虚拟屏尺寸等（看板按真实比例画显示器） */
export interface RuntimeInfo {
  display: { width: number; height: number };
  record: { fps: number; crf: number };
  maxSessions: number;
  idleTimeout: string;
}

/** AI 用完工作流后留下的建议（产品改进输入，不属于验收证据） */
export interface Feedback {
  id: number;
  at: string;
  sessionId: string;
  source: string;
  rating: number;
  body: string;
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
  stopSession: (id: string, verdict?: string, summary?: string) =>
    post<Session>(`/api/sessions/${id}/stop`, { verdict, summary }),
  listEvents: (id: string) => get<{ events: SessionEvent[] }>(`/api/sessions/${id}/events`),
  metrics: () => get<{ samples: Sample[] }>('/api/system/metrics'),
  payloads: () => get<PayloadsResponse>('/api/payloads'),
  machines: () => get<{ machines: Machine[] }>('/api/machines'),
  info: () => get<RuntimeInfo>('/api/info'),
  feedback: () => get<{ feedback: Feedback[] }>('/api/feedback?limit=20'),
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
