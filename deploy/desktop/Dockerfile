# syntax=docker/dockerfile:1
#
# Amber 的「一次性机器」运行时镜像。
#
# 构建（上下文就是这个目录，不带整个仓库）：
#   make image        # = docker build --network=host -t rigd-desktop:latest deploy/desktop
#
# 以前这份 Dockerfile 住在 v0 仓库 electron-acceptance-rig 里。那个仓库已废弃，
# 但 rigd 每天起的容器用的正是它构建出来的镜像——镜像有源头、源头没人维护，
# 想改桌面就得回废弃仓库改。所以搬到这里，rigd 自包含。
#
# 定位：只提供「一块能跑 GUI 的屏幕 + 录屏」。被测产物由控制面只读挂到 /app，
# 容器里没有 git、没有 node、没有构建工具链——"怎么产出这个产物"是调用方的事。
#
# 为什么用 Arch 而不是 Ubuntu：产物是在**构建机**上编出来的，原生模块（node-gyp 之类）
# 会钉死构建时的 glibc 版本。运行时比构建机旧就会直接
# `GLIBC_2.4x not found`——这不是应用的问题，是"运行时太老"。
# 用和构建机同源的发行版，这条缝就不存在了；换机器时改 FROM 即可。

FROM archlinux:latest

# --- 系统依赖 ---------------------------------------------------------------
# 分三组：虚拟显示与录屏 / 窗口管理 / GUI 运行时库
RUN pacman -Sy --noconfirm --needed \
      xorg-server-xvfb xorg-xdpyinfo xorg-xprop xorg-xwininfo \
      openbox wmctrl xdotool \
      ffmpeg jq curl procps-ng dbus \
      ttf-dejavu ttf-liberation noto-fonts-cjk \
      gtk3 nss alsa-lib libcups libdrm mesa libxkbcommon \
      libxcomposite libxdamage libxfixes libxrandr libxtst libxss \
      libnotify libsecret pango cairo libx11 libxcb \
      && pacman -Scc --noconfirm

# 虚拟桌面的底色：整屏录像里「窗口之外」那片区域要有桌面色，不能是 Xvfb 默认的黑——
# 黑色会让人以为画面被裁过，其实那就是桌面本身。
# feh 负责铺壁纸（xsetroot 只会铺纯色），xsetroot 留作兜底。
# 包库走国内镜像：官方 geo 镜像实测 70KB/s，同步一次 db 要几分钟，构建会卡在「Synchronizing」。
RUN printf 'Server = https://mirrors.ustc.edu.cn/archlinux/$repo/os/$arch\n' > /etc/pacman.d/mirrorlist \
      && pacman -Sy --noconfirm --needed xorg-xsetroot feh \
      && pacman -Scc --noconfirm

# 桌面壁纸本身：和 Amber 的锁屏同一套配色，所以录出来的画面和看板上的机器是一个世界。
# 不用 COPY --chmod：那样会连新建的目录一起改成 0644（少了 x 位，进不去），
# 必须让 install -D 来建目录。
COPY assets/wallpaper.jpg /tmp/wallpaper.jpg
RUN install -D -m 0644 /tmp/wallpaper.jpg /usr/share/rig/wallpaper.jpg && rm -f /tmp/wallpaper.jpg

# --- 普通用户 ---------------------------------------------------------------
# uid 1000：产物直接落到宿主挂载目录时属主就是宿主用户，不用事后 chown
RUN useradd -m -u 1000 -s /bin/bash rig
ENV HOME=/home/rig

# --- 容器内脚本 -------------------------------------------------------------
COPY --chmod=0755 bin/ /usr/local/bin/
COPY --chmod=0755 entrypoint.sh /usr/local/bin/rig-entrypoint

RUN mkdir -p /app /out && chown -R rig:rig /app /out

USER rig
WORKDIR /home/rig
ENTRYPOINT ["/usr/local/bin/rig-entrypoint"]
CMD ["rig-serve"]
