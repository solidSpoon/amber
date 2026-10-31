#!/usr/bin/env bash
# 容器入口：搭好虚拟屏 + 窗口管理器（可选 VNC），然后跑传进来的命令（默认 rig-serve）。
#
# 这里没有任何"取代码 / 构建 / 装依赖"的动作：被测产物由控制面挂到 /app。
# 收尾顺序是硬要求：先让命令自己停录屏（SIGINT 收尾写 moov），再关应用、WM、Xvfb。
set -euo pipefail

RIG_W=${RIG_W:-1280}
RIG_H=${RIG_H:-800}
DISPLAY_NUM=${DISPLAY_NUM:-99}
export DISPLAY=":${DISPLAY_NUM}"
export RIG_OUT="${RIG_OUT:-/out}"

mkdir -p "$RIG_OUT"/{screenshots,video,shots,clips,logs,report,artifacts,userdata}

log() { printf '[rig] %s\n' "$*"; }

# --- 1. 虚拟屏 ---------------------------------------------------------------
# 虚拟屏尺寸就是约定值，不让应用自己改分辨率：录屏的取景是按这个尺寸定死的，
# 屏幕一被改，抓到的画面和取景对不上。切全屏只该改窗口，不该改屏幕。
Xvfb "$DISPLAY" -screen 0 "${RIG_W}x${RIG_H}x24" -nolisten tcp -ac \
  -extension RANDR +extension GLX +render \
  >"$RIG_OUT/logs/xvfb.log" 2>&1 &
XVFB_PID=$!

for _ in $(seq 1 100); do
  [ -e "/tmp/.X11-unix/X${DISPLAY_NUM}" ] && break
  sleep 0.1
done
[ -e "/tmp/.X11-unix/X${DISPLAY_NUM}" ] || { log "Xvfb 启动失败，见 logs/xvfb.log"; exit 1; }
log "虚拟屏就绪：${RIG_W}x${RIG_H} on ${DISPLAY}"

# --- 2. 窗口管理器 -----------------------------------------------------------
# 没有 WM 时 Electron 窗口不会被正确映射/聚焦，合成事件也容易丢
dbus-launch --sh-syntax >"$RIG_OUT/logs/dbus.env" 2>/dev/null || true
if [ -s "$RIG_OUT/logs/dbus.env" ]; then set -a; . "$RIG_OUT/logs/dbus.env"; set +a; fi
openbox >"$RIG_OUT/logs/openbox.log" 2>&1 &
OB_PID=$!
# 根窗口底色要等 openbox 真正接管后再铺：它在启动时会把根窗口重置成黑，
# 抢在前面铺就白铺了——录出来一片黑，看着像画面被裁过，其实那只是桌面。
for _ in $(seq 1 50); do
  xprop -root _NET_SUPPORTING_WM_CHECK 2>/dev/null | grep -q 'window id' && break
  sleep 0.1
done
# 壁纸优先；feh 起不来（缺包/图坏了）就退回纯色，录像不会变成一整块黑
if [ -f "${RIG_WALLPAPER:-/usr/share/rig/wallpaper.jpg}" ]; then
  feh --bg-fill "${RIG_WALLPAPER:-/usr/share/rig/wallpaper.jpg}" 2>/dev/null \
    || xsetroot -solid "${RIG_DESKTOP_COLOR:-#e8e6e3}" 2>/dev/null || true
else
  xsetroot -solid "${RIG_DESKTOP_COLOR:-#e8e6e3}" 2>/dev/null || true
fi
log "窗口管理器就绪：openbox"

# --- 3. VNC（可选） ----------------------------------------------------------
if [ "${RIG_VNC:-0}" = "1" ]; then
  x11vnc -display "$DISPLAY" -forever -shared -nopw -rfbport 5900 -quiet \
    >"$RIG_OUT/logs/x11vnc.log" 2>&1 &
  websockify --web=/usr/share/novnc "${RIG_VNC_PORT:-6080}" localhost:5900 \
    >"$RIG_OUT/logs/novnc.log" 2>&1 &
  log "noVNC 就绪：http://<host>:${RIG_VNC_PORT:-6080}/vnc.html?autoconnect=1"
fi

# --- 4. 执行命令 -------------------------------------------------------------
cleanup() {
  trap - EXIT INT TERM
  kill "$OB_PID" "$XVFB_PID" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# 命令在后台跑 + 显式转发信号。
# 原因：容器里 PID 1 是本脚本，docker stop 的 SIGTERM 只送到 PID 1，bash 不会自动转给前台子进程；
# 子进程收不到 TERM 就无法停录屏，最后被 SIGKILL 打死 → mp4 的 moov 索引写不完、文件报废。
"$@" &
CHILD=$!
trap 'kill -TERM "$CHILD" 2>/dev/null || true' TERM INT
wait "$CHILD"
RC=$?
log "命令结束：exit=${RC}"
exit "$RC"
