# 项目概览

`rigd` 是验收流水线的控制面：一台常驻的 Go 服务 + 一个 React 管理台。
它把「一次性容器 = 一台可扔的测试电脑」这件事做成可调度、可审计、可回看的会话。

## 为什么要从 v0 重构

v0（`electron-acceptance-rig`）把 agent 塞进了容器，能用，但代价明确：

| 问题 | v0 | v1 |
| --- | --- | --- |
| 模型/prompt 演进 | 改镜像、重建 | 不碰镜像，外部 AI 通过 MCP 驱动 |
| API key | 进容器 | 只在宿主进程内存里 |
| 审计 | 容器内 jsonl，出容器就断 | 控制面统一落库，天然带会话与时间线 |
| 并行 | 手动错端口 | 会话调度器分配端口与显示 |
| 回看 | 翻 runs 目录 | Web 管理台：会话列表 → 录像 + 每步操作可跳转 |

容器层（Xvfb、录屏、依赖缓存卷、镜像）**原样复用**，v0 保留为回归基线。

## 架构

```
        ┌───────────────────────────┐
        │ 外部 AI（任意 MCP 客户端）  │
        └─────────────┬─────────────┘
                      │ MCP (stdio, JSON-RPC)
┌─────────────────────▼─────────────────────────────┐
│ rigd (Go, 单进程)                                  │
│  ├─ mcp        工具面：start/snapshot/click/…      │
│  ├─ scheduler  会话注册表：容器、端口、超时回收      │
│  ├─ inspect    通过 CDP 读结构 / 点 / 输 / 截图     │
│  ├─ store      SQLite：会话、事件、产物、指标采样    │
│  ├─ httpapi    REST + SSE + 静态资源（内嵌 React）   │
│  └─ telemetry  gopsutil：CPU/内存/磁盘 + 容器统计    │
└─────────────┬─────────────────────────────────────┘
              │ docker CLI（可换成官方 SDK）
     ┌────────▼────────┐
     │ 一次性容器       │  Xvfb + openbox + ffmpeg 录屏
     │ Electron 应用    │  --network host → CDP 端口直接可达
     └─────────────────┘
```

关键点：容器用 `--network host`，调试端口直接落在宿主 loopback，
所以 **CDP 由 Go 侧直接驱**，不需要在容器里塞 Node/Playwright。

## 目录

```
cmd/rigd/           进程入口（只做装配，不放业务）
internal/
  config/           环境变量 → 结构化配置，启动即校验
  store/            SQLite + 内嵌迁移；会话/事件/产物/指标
  dockerx/          容器运行时抽象（当前实现走 docker CLI）
  session/          会话调度：起容器、分配端口、超时回收、收尾
  cdp/              裸 CDP 客户端（websocket + JSON-RPC）
  inspect/          在 cdp 之上：结构快照、点击、输入、截图
  telemetry/        宿主与容器指标采样
  httpapi/          REST + SSE + 内嵌前端
  mcp/              MCP 工具面
web/                React 管理台（Vite + TS + Tailwind）
deploy/             Dockerfile、systemd unit
```

## 本地开发

```bash
make dev          # 后端 :8088 + 前端 vite dev :5173（代理 /api）
make test         # go test ./...
make lint         # golangci-lint
make web          # 构建前端并内嵌进二进制
make build        # 产出 bin/rigd（含前端）
```

## 约定

- 配置只从环境变量读，缺关键项启动即失败（不静默兜底）。
- 所有对外状态都在 SQLite 里，进程重启后会话列表与录像索引不丢。
- HTTP 与 MCP 共用同一套 `session.Manager`，两边行为不会漂移。
- 容器生命周期由调度器独占管理，API 不直接碰 docker。
