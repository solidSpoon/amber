// 应用注册表：把「应用标识」映射成容器需要的环境变量。
//
// 为什么放在 Go 里而不是配置文件：应用适配是代码级契约（容器里必须存在对应脚本），
// 写死在注册表里能保证「配错立即失败」，而不是运行时才发现路径不存在。
// 新增应用 = 加一条记录 + 在镜像里放对应脚本，两处都要改，这本身就是一道校验。
package session

import (
	"fmt"
	"sort"
)

// AppSpec 一个被测应用的容器侧契约。
type AppSpec struct {
	// Name 展示名，也用于报告标题。
	Name string
	// Description 一句话说明这个应用是干什么的，给控制台做选择器文案。
	Description string
	// Repo 默认代码仓库（会话不传仓库时使用）。
	Repo string
	// AppDir 应用在容器内的目录。
	AppDir string
	// BuildScript 容器内的准备脚本（装依赖、构建、铺素材）。
	BuildScript string
	// Bootstrap 打开应用后的前置动作（跳过引导、打开测试素材）。
	Bootstrap string
	// NodeModulesVolume 依赖缓存卷：挂到 AppDir/node_modules，跨会话复用。
	NodeModulesVolume string
	// YarnCacheVolume 包缓存卷：挂到 /cache/yarn，换分支时只补差额。
	YarnCacheVolume string
	// ElectronCacheVolume Electron 发行包缓存卷：挂到 /opt/electron-cache。
	ElectronCacheVolume string
	// RequiresMedia 应用是否需要验收素材（视频/字幕），需要则必须配置 RIGD_MEDIA_DIR。
	RequiresMedia bool
}

// builtinApps 内置应用表。key 是 API/MCP 里传的 app 名。
var builtinApps = map[string]AppSpec{
	"DashPlayer": {
		Name:        "DashPlayer",
		Description: "Electron 视频播放器（带字幕翻译流水线）",
		Repo:        "https://github.com/solidSpoon/DashPlayer.git",
		AppDir:      "/app",
		BuildScript: "/opt/apps/dashplayer/build.sh",
		Bootstrap:   "/opt/apps/dashplayer/bootstrap.mjs",
		// 卷名沿用 v0 已经预热好的那一份：2.6G 依赖 + 3.7G 包缓存，别让重构把缓存作废
		NodeModulesVolume:   "rig-dashplayer-node-modules",
		YarnCacheVolume:     "rig-dashplayer-yarn-cache",
		ElectronCacheVolume: "rig-electron-cache",
		RequiresMedia:       true,
	},
	// 示例应用：镜像自带，用来做控制面自检（不需要任何仓库）。
	"sample": {
		Name:        "Rig Sample App",
		Description: "镜像自带的示例应用，用来自检控制面（不需要仓库）",
		AppDir:      "/opt/sample-app",
	},
}

// LookupApp 按名字取应用规格；未知应用直接报错，不做任何猜测。
func LookupApp(name string) (AppSpec, error) {
	spec, ok := builtinApps[name]
	if !ok {
		return AppSpec{}, fmt.Errorf("未知应用 %q（已注册：%v）", name, KnownApps())
	}
	return spec, nil
}

// AppEntry 注册表里的一条：Key 是会话/API 里传的标识，Spec 是容器侧契约。
type AppEntry struct {
	Key  string
	Spec AppSpec
}

// AppCatalog 返回全部应用，按标识排序，供控制台渲染应用选择器。
//
// 返回副本而不是 map 本身：调用方拿到的必须是只读快照，不能反手改注册表。
func AppCatalog() []AppEntry {
	entries := make([]AppEntry, 0, len(builtinApps))
	for key, spec := range builtinApps {
		entries = append(entries, AppEntry{Key: key, Spec: spec})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// KnownApps 返回已注册的应用名，用于错误提示与文档。
func KnownApps() []string {
	names := make([]string, 0, len(builtinApps))
	for name := range builtinApps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
