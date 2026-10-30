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

## 画面链路（取景 → 传输 → 显示）

录像是这套东西的证据，所以「录什么」和「怎么看」是同一条链路上的两件事，必须一起设计：

```
容器：启动应用 → 铺满虚拟屏（rig-fit-window）→ 按窗口取景开录（rig-record）
        ↓ logs/window.json（窗口矩形 + 虚拟屏尺寸）
控制面：就绪时存进会话（frame / display）
        ↓ GET /api/sessions/{id} 带上 frame+display；/poster 给首帧静帧
管理台：ScreenStage 按 frame/display 裁剪 → 黑底等比显示，不裁变形、不铺一张大空白
```

几条不能反的顺序：

- **先铺满，再开录**。录屏抓的是整个虚拟屏（这样弹窗、菜单、原生对话框都不会漏），
  窗口没铺满就先开录，整段录像都会是「角落里一个小窗 + 一大片空桌面」。
  铺满走容器内的 WM（`wmctrl`），不走 CDP：`Browser` 域只挂在浏览器端点上，
  从页面目标发 `Browser.getWindowForTarget` 会直接报 `wasn't found`。
- **就绪 = CDP 可达 + 取景已定 + 录屏已开**。容器落 `logs/ready.json` 之后才打 `READY`，
  控制面等这个文件，否则前端会拿到一个还没开始录的会话（偏移量全是 -1）。
- **铺不满也要有兜底**：应用拒绝最大化时，`rig-record` 会退化成「只抓窗口那一块」，
  并把真实矩形写进 `window.json`；界面据 `frame != display` 裁剪并说明原因。
- **裁剪只是渲染层的变换**：放大 `display/frame` 倍 + 平移，录像本身一个字节不动，
  时间轴偏移量也不受影响。

控制面需要 `ffmpeg` 在 PATH 上：只用来给录像抽首帧封面。没装也能跑，
只是录像卡片在按下播放前是一块黑屏。

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
web/                React 管理台（Vite + TS + Tailwind + shadcn/ui）
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

## 管理台

- 组件库用 shadcn/ui（`base-nova` 预置，Base UI + Tailwind v4），组件是源码进仓库的，
  加组件走 `cd web && npx shadcn@latest add <组件>`，不要手写等价物。
- **设计必须项目中立**：控制台服务的是"任意 GUI 应用"，不针对某一个被测应用做定制。
  应用清单来自 `GET /api/apps`（后端 `session` 包的应用注册表），前端不写死任何应用名、
  不假设某一个应用的页面结构或素材；文案里也不出现具体项目名。
- 颜色只用语义 token（`bg-background` / `text-muted-foreground` / `bg-muted`…），
  状态色用 `success` / `warning` / `destructive` 这几个变体，不写 `text-emerald-600` 这类原始色值。
- 画面一律走 `ScreenStage`（`web/src/components/screen-stage.tsx`），别直接铺 `<video>`/`<img>`：
  它负责按 session 的 `frame`/`display` 裁剪、等比、黑底，换成别的写法就会退回
  「白底一张大图、应用缩在角落」那种样子。

## 约定

- 配置只从环境变量读，缺关键项启动即失败（不静默兜底）。
- 所有对外状态都在 SQLite 里，进程重启后会话列表与录像索引不丢。
- HTTP 与 MCP 共用同一套 `session.Manager`，两边行为不会漂移。
- 容器生命周期由调度器独占管理，API 不直接碰 docker。
