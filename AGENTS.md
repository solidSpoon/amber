# 给 agent 的工作约定

这个文件是"在这个仓库干活之前必须先知道的坑"。不是文档，是经验账——踩过一次就别踩第二次。

## 时间去哪了（2026-10-30 复盘）

一次"改容器镜像 + 改数据模型 + 改前端"的活儿干了几个小时，其中**真正写代码不到 20 分钟**：

| 耗时 | 花在哪 | 能不能避免 |
| --- | --- | --- |
| ~35 min | 容器镜像重建（三次） | 能，见下面两条 |
| ~10 min | glibc 不匹配导致基座返工 | 能，先问清"产物在哪构建" |
| ~5 min | 反复「编译 + 重启 + 起容器 + 截图」验证 | 能，合成一条脚本 |
| ~20 min | 写代码（这一步是正常的） | — |

### 1. 长任务必须后台化，且用 nohup + setsid + 重定向 stdin

`docker build`、`npm install`、大体积上传这类超过一两分钟的命令，**不要**让工具窗口扛：

```bash
setsid nohup bash -c 'docker build ... > /tmp/build.log 2>&1; echo EXIT=$? >> /tmp/build.log' \
  >/dev/null 2>&1 </dev/null & disown
```

然后 `sleep N; tail /tmp/build.log` 轮询。

- 工具超时会把命令杀掉，**杀掉 = 前面所有构建层全废**（一次白等 15 分钟就是这么来的）。
- 只 `&` 后台化、不 `nohup`、不重定向 stdin，命令结束时仍会被连带杀掉（实测：casual 后台构建在命令返回后收到 canceled）。

### 2. 动镜像之前，先做 30 秒预检

- **测速**：`curl -s -o /dev/null -w '%{speed_download}' <mirror>/archlinux/extra/os/x86_64/extra.db`。
  这台机器上 docker **bridge 网络很慢**，构建要加 `--network=host`；镜像源用国内源
  （ustc / tuna，实测 20–30 MB/s，官方源会卡在 "Retrieving packages..."）。
- **算体积**：`Total Installed Size` 超过 1GB 就要先想清楚是不是真要装（noto-fonts-cjk、mesa、gtk3 是大头）。

### 3. 产物与运行时是同一个 glibc 契约

被测产物在**哪台机器编**，运行时就得至少一样新。宿主 Arch（glibc 2.44）编出来的 Electron
原生模块，在 Ubuntu 24.04（glibc 2.39）里直接 `GLIBC_2.44 not found`。

所以运行时镜像（`deploy/desktop`，`make image` 构建）用 Arch 基座，和构建机同源。
**改基座之前先问**：这个产物是在什么环境构建的？

### 4. 一次只改一层

Go 编译 0.5 秒不是瓶颈，"验证"才是。改 store 表结构 → session → http → mcp → web 五层一起动，
每轮都要「编译 + 重启 + 起会话 + 截图」，成本被放大。先把接口定死，一次写完，再统一编译。

### 5. 验证要有脚本，不要手工敲

起会话、跑动作、断言、截图这一套每次都手敲 curl，容易漏步骤也慢。
写一个 `scripts/smoke.sh` 固化下来（会话 ID 落文件、断言失败立刻退出），一次跑完。

## 项目约定

- **产品的定义在 `docs/PRODUCT.md`，页面上有同一份（`#/docs`）。** 那里只讲产品不讲实现，
  但它是准绳：代码里看着"本该如此"的东西可能是历史包袱，两者冲突时以文档为准；
  要改产品行为，先改文档。

- **控制台不做人工操作入口**：这是给 AI 用的应用，人只看报告和看板。
  不要加"新建会话 / 手动点这里"的表单类控件。
- 报告只能从 `Record()` 闸门流出来，不允许事后另写一份（两边必然对不上）。
- 内容寻址存储（`internal/store/blobs.go`）：产物/素材按 sha256 存，会话用 manifest 引用；
  `dir:` 来源只允许 `RIGD_WORKSPACE_ROOTS` 白名单内的路径，且只读挂载。
- 容器里没有 git / node / 构建工具链。想"让它构建一下"就是走错方向了。
