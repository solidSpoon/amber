# 项目概览

`rigd`（产品名 **Amber**，代码与二进制沿用这个内部代号）是验收流水线的控制面：一台常驻的 Go 服务 + 一个 React 管理台。
它把「一次性容器 = 一台可扔的测试电脑」这件事做成可调度、可审计、可回看的会话。

## 为什么要从 v0 重构

v0（`electron-acceptance-rig`）把 agent 塞进了容器，能用，但代价明确：

| 问题 | v0 | v1 |
| --- | --- | --- |
| 模型/prompt 演进 | 改镜像、重建 | 不碰镜像，外部 AI 通过 MCP 驱动 |
| API key | 进容器 | 只在宿主进程内存里 |
| 审计 | 容器内 jsonl，出容器就断 | 控制面统一落库，天然带会话与时间线 |
| 并行 | 手动错端口 | 会话调度器分配端口与显示 |
| 回看 | 翻 runs 目录 | Web 管理台：会话列表 → 一段一段的步骤记录 + 每段的画面 |

容器层（Xvfb、录屏、依赖缓存卷、镜像）**原样复用**，v0 保留为回归基线。

## 架构

```
        ┌───────────────────────────┐
        │ 外部 AI（任意 MCP 客户端）  │
        └─────────────┬─────────────┘
                      │ MCP (stdio, JSON-RPC)
┌─────────────────────▼─────────────────────────────┐
│ rigd (Go, 单进程)                                  │
│  ├─ mcpserver  工具面：start/run/capture/…        │
│  ├─ scheduler  会话注册表：容器、端口、超时回收      │
│  ├─ inspect    通过 CDP 读结构 / 点 / 输 / 截图     │
│  ├─ store      SQLite：会话、事件、内容寻址、采样    │
│  ├─ httpapi    REST + 静态资源（内嵌 React，只读）  │
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
        ↓ GET /api/sessions/{id} 带上 frame+display
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

控制面需要 `ffmpeg` 在 PATH 上：收尾时把每一批那几秒切成片段（整场录像不留），
以及会话还在跑时按需现切。

## 记录（产物形态）

验收产物不是一条长录像，而是一份可以顺着读的**步骤记录**。录像只作为证据留下来：
每一批连续操作配一条几秒的片段，点开就是那一段，不用在时间里搜。

```
每次工具调用 → Record() 闸门 → 一条事件（caption/intent/expect/result/shot）
                                   ↓
收尾：按批次切片段（cutClips）→ 删掉整场录像
记录页：一段一段读——这段做了什么 + 这一批收尾那张截图 + 这一段的画面
附录：运行日志
```

几条硬规则：

- **记录只能从 `Record()` 流出来**。不允许事后另写一份：两条链路一定会对不上，而对不上的记录比没有更能骗人。
- **caption 自动生成**，从可访问性树拿 `role`/`name`（`点击「保存」按钮`）。模型只负责 `intent`（为什么）、`expect`（期望什么），没写就空缺，不编。
- **截图只给"需要看现场"的步骤**：失败的那一步，和 `capture`（这一步的价值就是那张图）——
  每一步都截一张的话，一批 30 步光截图就多花好几秒，而记录里并不逐行展示它们。
  点元素时会把元素框高亮出来（Steps Recorder 那个红圈），失败也截——读者最想看的就是出错时的屏幕。
- **画面截的是整块屏幕，不是网页**。原生文件对话框、系统菜单不在页面里，
  用 CDP 的 `Page.captureScreenshot` 拍不到它们——而那正是最需要看的现场。
  所以步骤图由 `session/screen.go` 在容器里对虚拟屏抓帧（和录屏同一块屏）。
- **整场录像不留**：没人会从头看一遍，而它又大又慢。所以收尾时先切片段、再删母片。
- 片段的窗口是**这一批第一步前 2 秒到最后一步后 2 秒**，不做动画检测：可预期、不出怪，够用。

## 目录

```
cmd/rigd/           进程入口（只做装配，不放业务）
internal/
  config/           环境变量 → 结构化配置，启动即校验
  store/            SQLite + 内嵌迁移；会话/事件/内容寻址库/指标
  dockerx/          容器运行时抽象（当前实现走 docker CLI）
  session/          会话调度：起容器、分配端口、超时回收、收尾
  cdp/              裸 CDP 客户端（websocket + JSON-RPC）
  inspect/          在 cdp 之上：结构快照、点击、输入、框选元素
  telemetry/        宿主与容器指标采样
  httpapi/          REST + 内嵌前端（只读：操作面只有 MCP）
  mcpserver/        MCP 工具面
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
  会话的应用名来自产物自己的 `rig.json`（`name`），前端不写死任何应用名、
  不假设某一个应用的页面结构或素材；文案里也不出现具体项目名。
- 颜色只用语义 token（`bg-background` / `text-muted-foreground` / `bg-muted`…），
  状态色用 `success` / `warning` / `destructive` 这几个变体，不写 `text-emerald-600` 这类原始色值。
- 画面一律走 `ScreenStage`（`web/src/components/screen-stage.tsx`），别直接铺 `<video>`/`<img>`：
  它负责按 session 的 `frame`/`display` 裁剪、等比、黑底，换成别的写法就会退回
  「白底一张大图、应用缩在角落」那种样子。

## MCP 工具面

六个工具，一个动词只做一件事（真正的契约在 `internal/mcpserver` 的
`actionTable` 与 `runInputSchema`，这里只解释怎么读它）：

| 工具 | 干什么 |
| --- | --- |
| `amber_start` | 起一台一次性机器：容器 + 应用 + 全程录屏（`title` 必填，一句话说明这次验收的意图） |
| `amber_run` | 执行一串动作（1~50 步），最后一步必须是 `capture`（`intent` 必填，记录里当这一段的小标题） |
| `amber_capture` | 看一眼当前界面，并记成记录里的一步（返回元素表 + 整屏画面） |
| `amber_stop` | 收尾：停录屏、切片段、销毁容器（不写结论，判断留给读记录的人） |
| `amber_list` | 看最近会话与状态 |
| `amber_feedback` | 对工作流本身提意见（不进记录） |

`run` 的每一步是「动作名 + 这个动作自己的字段」，只有 `action` 必填。
**动作清单和每个动作的必填字段不在本文里复述**——它就在 `internal/mcpserver/actions.go`
的 `actionTable` 里（`Name` / `Need` / `Note` / `Check` / `Run`），
JSON Schema 的枚举、工具说明里的对照表、执行前的自检全部由它生成。
文档再抄一份，就一定会有一份先过期（历史上就出现过说明里写着 `assert`、代码里没有分支）。

**意图是必填的**：`amber_start.title` 是这次验收的标题（会话列表和记录顶部展示它），
`run.intent` 是这一段操作的标题。原来记录里的小标题是从动作名拼出来的
（「点击「Next」→ 点击「Skip」」），那是"做了什么"，不是"为什么"——拼不出来的就不编。

两条纪律：

- **`wait` 只等条件、`sleep` 只等时间**，写混了当场报错并告诉该用哪个。
  以前"等 3 秒"只能写 `wait(for:"true")`，那是等 0 秒——语义正好反了。
- **`capture` 是唯一的"看一眼"**。历史上 `snapshot` / `wait_and_capture` 是同一个东西的
  两个名字，现在只宣传 `capture`；旧名字还能用，但不再出现在工具清单里。

## 约定

- 配置只从环境变量读，缺关键项启动即失败（不静默兜底）。
- 所有对外状态都在 SQLite 里，进程重启后会话列表与录像索引不丢。
- 控制台是只读的：查会话、看记录、取图片。点按/填表/求值只走 MCP 一条路，
  同一个动作有两条实现，就一定会有一条先漂（工具名、审计文案、重试策略各不相同）。

- HTTP 与 MCP 共用同一套 `session.Manager`（会话生命周期、审计、收尾），状态不会分叉。
- 容器生命周期由调度器独占管理，API 不直接碰 docker。
