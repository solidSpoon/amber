package session

// payload.go：输入物是「可运行的产物」，不是「仓库 + 分支」。
//
// 两种来源：
//   dir:/abs/path      宿主目录，只读挂载（同机、体积大、零拷贝）
//   manifest:sha256:…  已上传的目录树，从内容寻址库里物化出来（跨机、可复现）
//
// 应用自述（rig.json，可选）告诉容器怎么启动、怎么算就绪、需要哪些素材槽位。
// 控制面只认识「路径 / 名字 / sha256」，不认识"模型""素材"这种业务概念——这就是项目中立。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solidSpoon/rigd/internal/store"
)

// Recipe 是产物根目录下 rig.json 的内容（没有也能跑，靠约定猜入口）。
type Recipe struct {
	Name  string            `json:"name"`
	Entry string            `json:"entry"`
	Args  []string          `json:"args"`
	Env   map[string]string `json:"env"`
	Ready ReadySpec         `json:"ready"`
	// Assets 声明"我需要哪些外部目录"。槽位名由应用自己起，rigd 不解释含义。
	Assets []AssetSlot `json:"assets"`
}

// ReadySpec 怎么算"应用起来了、可以开始操作了"。
type ReadySpec struct {
	// CDP 调试端口（Electron/Chromium 系）；0 表示只看窗口是否出现
	CDP        int `json:"cdp"`
	TimeoutSec int `json:"timeoutSec"`
}

// AssetSlot 一个素材槽位：挂到容器里的哪个路径。
type AssetSlot struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Required bool   `json:"required"`
	// Writable 素材目录是否可写。
	//
	// 默认只读是对的（素材被会话写坏，下次验收就不是同一份输入了）。但很多应用
	// 会把"目录不可写"直接判成"没有访问权限"，然后弹一个原生授权窗等人点——
	// 在无人操作的机器上就是永久等待。这类应用（媒体库、素材库、下载目录）
	// 必须在 rig.json 里明写 writable，否则它连正常启动都做不到。
	Writable bool `json:"writable"`
	// Env 可选：把挂载路径写进这个环境变量，免得应用找不到
	Env string `json:"env"`
}

// Source 一个输入物的来源。
type Source struct {
	Kind string // dir | manifest
	// Path 宿主目录（Kind=dir）
	Path string
	// Digest 已上传的目录树（Kind=manifest，裸 hex）
	Digest string
	// Raw 调用方原始写法，错误信息里原样回显
	Raw string
}

// ParseSource 解析 "dir:/abs" / "manifest:sha256:…"。
func ParseSource(raw string) (Source, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Source{}, errors.New("来源不能为空")
	}
	if rest, ok := strings.CutPrefix(value, "dir:"); ok {
		path := filepath.Clean(strings.TrimSpace(rest))
		if !filepath.IsAbs(path) {
			return Source{}, fmt.Errorf("dir 来源必须是绝对路径：%q", raw)
		}
		return Source{Kind: "dir", Path: path, Raw: value}, nil
	}
	if rest, ok := strings.CutPrefix(value, "manifest:"); ok {
		digest, err := store.NormalizeDigest(rest)
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: "manifest", Digest: digest, Raw: value}, nil
	}
	return Source{}, fmt.Errorf("无法识别的来源 %q（支持 dir:/绝对路径 或 manifest:sha256:…）", raw)
}

// payloadInput 解析后的产物：宿主机上的一个目录 + 它的自述。
type payloadInput struct {
	Source Source
	// Dir 该目录在宿主上的真实位置（dir 来源 = 原目录；manifest 来源 = 物化出来的目录）
	Dir    string
	Recipe Recipe
	// Entry 相对 Dir 的可执行文件
	Entry string
	// Mount 需要额外挂载的素材：槽位 → 宿主目录
	Assets []assetMount
}

type assetMount struct {
	Slot AssetSlot
	Dir  string
}

// resolvePayload 校验来源、物化产物、读自述、确定入口。
// materialize=true 表示"从源把字节铺出来"（Prepare 用）；Provision 里传 false，
// 因为快照在上一步已经做好了——再铺一遍会把快照里的硬链接当成普通文件覆盖，
// 甚至顺着链接把宿主上的原文件截断。
func (m *Manager) resolvePayload(ctx context.Context, dir, raw string, assets map[string]string, materialize bool) (payloadInput, error) {
	src, err := ParseSource(raw)
	if err != nil {
		return payloadInput{}, err
	}

	input := payloadInput{Source: src}
	if src.Kind == "dir" {
		if err := m.checkWorkspacePath(src.Path); err != nil {
			return payloadInput{}, err
		}
		info, statErr := os.Stat(src.Path)
		if statErr != nil || !info.IsDir() {
			return payloadInput{}, fmt.Errorf("产物目录不存在：%s", src.Path)
		}
		// 起会话就把目录"定格"：硬链接快照到会话目录，不占额外磁盘。
		// 不做这一步的话，容器看的是宿主的实时视图——你在会话跑着的时候重新打包，
		// /app 会在半路变成"一半旧文件一半新文件"，这次验收的证据就废了。
		input.Dir = filepath.Join(dir, "payload")
		if materialize {
			if err := snapshotTree(src.Path, input.Dir, false); err != nil {
				return payloadInput{}, err
			}
		}
	} else {
		manifest, err := m.store.GetManifest(ctx, src.Digest)
		if err != nil {
			return payloadInput{}, fmt.Errorf("产物 manifest 不存在：%w", err)
		}
		input.Dir = filepath.Join(dir, "payload")
		if materialize {
			if err := m.materialize(manifest, input.Dir); err != nil {
				return payloadInput{}, err
			}
		}
	}

	if input.Recipe, err = readRecipe(input.Dir); err != nil {
		return payloadInput{}, err
	}
	if input.Entry, err = resolveEntry(input.Dir, input.Recipe); err != nil {
		return payloadInput{}, err
	}

	input.Assets, err = m.resolveAssets(ctx, dir, input.Recipe.Assets, assets, materialize)
	if err != nil {
		return payloadInput{}, err
	}
	return input, nil
}

// checkWorkspacePath 只允许挂载配置里认可的根目录：防止"给我跑一下 /etc"。
func (m *Manager) checkWorkspacePath(path string) error {
	roots := m.cfg.WorkspaceRoots
	if len(roots) == 0 {
		return errors.New("未配置 RIGD_WORKSPACE_ROOTS，dir 来源已禁用（请改用 manifest:… 上传产物）")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(rootAbs, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return nil
		}
	}
	return fmt.Errorf("路径 %s 不在允许的目录范围内（RIGD_WORKSPACE_ROOTS）", abs)
}

// materialize 把目录树从内容寻址库里"铺"出来。
//
// 用硬链接而不是复制：1.3GB 的产物铺出来是零拷贝、毫秒级；blob 本身只读，
// 不会被会话写坏（会话里挂的是只读挂载）。
func (m *Manager) materialize(manifest store.Manifest, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("创建产物目录失败: %w", err)
	}
	for _, entry := range manifest.Entries {
		target := filepath.Join(dst, entry.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		// 可执行位要显式补上：blob 是按内容存的，里面没有"这个文件可执行"这回事
		mode := os.FileMode(0o644)
		if entry.Mode&0o111 != 0 {
			mode = 0o755
		}
		if err := copyOrLink(m.store.BlobPath(entry.Digest), target, mode, false); err != nil {
			return fmt.Errorf("铺开 %s 失败: %w", entry.Path, err)
		}
		_ = os.Chmod(target, mode)
	}
	return nil
}

// readRecipe 读产物根目录的 rig.json；没有就是空自述（靠约定猜）。
func readRecipe(dir string) (Recipe, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "rig.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Recipe{}, nil
	}
	if err != nil {
		return Recipe{}, fmt.Errorf("读 rig.json 失败: %w", err)
	}
	var recipe Recipe
	if err := json.Unmarshal(raw, &recipe); err != nil {
		return Recipe{}, fmt.Errorf("解析 rig.json 失败: %w", err)
	}
	for _, slot := range recipe.Assets {
		if slot.Name == "" || !filepath.IsAbs(slot.Path) {
			return Recipe{}, fmt.Errorf("rig.json 里的素材槽位必须同时给 name 和绝对 path：%+v", slot)
		}
	}
	if recipe.Ready.TimeoutSec <= 0 {
		recipe.Ready.TimeoutSec = 60
	}
	return recipe, nil
}

// resolveEntry 确定启动哪个文件：自述优先，否则按常见约定猜（猜不到就明确报错）。
func resolveEntry(dir string, recipe Recipe) (string, error) {
	if recipe.Entry != "" {
		entry := strings.TrimPrefix(filepath.Clean(recipe.Entry), "/")
		info, err := os.Stat(filepath.Join(dir, entry))
		if err != nil {
			return "", fmt.Errorf("rig.json 指定的入口不存在：%s", recipe.Entry)
		}
		if info.IsDir() {
			return "", fmt.Errorf("rig.json 指定的入口是目录：%s", recipe.Entry)
		}
		return entry, nil
	}

	guesses := []string{"AppRun", "start.sh", "run.sh"}
	for _, guess := range guesses {
		if info, err := os.Stat(filepath.Join(dir, guess)); err == nil && !info.IsDir() {
			return guess, nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("读产物目录失败: %w", err)
	}
	var executables, appImages []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if strings.HasSuffix(e.Name(), ".AppImage") {
			appImages = append(appImages, e.Name())
		} else if info.Mode()&0o111 != 0 {
			executables = append(executables, e.Name())
		}
	}
	sort.Strings(executables)
	sort.Strings(appImages)
	switch {
	case len(appImages) == 1:
		return appImages[0], nil
	case len(executables) == 1:
		return executables[0], nil
	}
	return "", errors.New("产物根目录找不到唯一入口，请在 rig.json 里写明 entry")
}

// resolveAssets 把调用方给的素材来源对到产物声明的槽位上。
func (m *Manager) resolveAssets(ctx context.Context, sessionDir string, slots []AssetSlot, provided map[string]string, materialize bool) ([]assetMount, error) {
	byName := make(map[string]AssetSlot, len(slots))
	for _, slot := range slots {
		byName[slot.Name] = slot
	}
	for name := range provided {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("产物没有声明素材槽位 %q（可用：%v）", name, slotNames(slots))
		}
	}

	mounts := make([]assetMount, 0, len(slots))
	for _, slot := range slots {
		raw, ok := provided[slot.Name]
		if !ok || strings.TrimSpace(raw) == "" {
			if slot.Required {
				return nil, fmt.Errorf("缺少必需素材槽位 %q（挂载点 %s）", slot.Name, slot.Path)
			}
			continue
		}
		src, err := ParseSource(raw)
		if err != nil {
			return nil, fmt.Errorf("素材 %q：%w", slot.Name, err)
		}
		mount := assetMount{Slot: slot}
		switch src.Kind {
		case "dir":
			if err := m.checkWorkspacePath(src.Path); err != nil {
				return nil, err
			}
			if info, err := os.Stat(src.Path); err != nil || !info.IsDir() {
				return nil, fmt.Errorf("素材目录不存在：%s", src.Path)
			}
			// 素材同样快照：一次会话里"给了哪些字节"必须是确定的。
			// 可写槽位不能走硬链接：写进去会直接改掉宿主上的原件，
			// 下次验收拿到的就不是同一份输入了。
			mount.Dir = filepath.Join(sessionDir, "assets", slot.Name)
			if materialize {
				if err := snapshotTree(src.Path, mount.Dir, slot.Writable); err != nil {
					return nil, err
				}
			}
		case "manifest":
			manifest, err := m.store.GetManifest(ctx, src.Digest)
			if err != nil {
				return nil, fmt.Errorf("素材 %q 的 manifest 不存在：%w", slot.Name, err)
			}
			mount.Dir = filepath.Join(sessionDir, "assets", slot.Name)
			if materialize {
				if err := m.materialize(manifest, mount.Dir); err != nil {
					return nil, err
				}
			}
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func slotNames(slots []AssetSlot) []string {
	names := make([]string, 0, len(slots))
	for _, slot := range slots {
		names = append(names, slot.Name)
	}
	return names
}

// decodeAssets 把库里存的 JSON 还原成槽位映射（坏数据返回空表，不 panic）。
func decodeAssets(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var assets map[string]string
	if err := json.Unmarshal([]byte(raw), &assets); err != nil {
		return nil
	}
	return assets
}

// snapshotTree 把目录树定格到会话目录：默认硬链接（零拷贝），copyFiles=true 时真复制。
//
// 硬链接而不是真复制：1.3GB / 一万个文件的产物大约 1 秒、零额外磁盘，
// 而且之后宿主上重新打包（写新文件或 rename 替换）不会透过链接影响这份快照。
// 跨文件系统（builder 输出在另一个挂载点）时自动退化成复制。
//
// 什么时候必须复制：这个目录之后要挂成可写。硬链接的两个名字指向同一个 inode，
// 容器里写文件就是写宿主上的原件——素材被改掉，下次验收的输入就不一样了。
func snapshotTree(src, dst string, copyFiles bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 不是目录", src)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("创建快照目录失败: %w", err)
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o755)
		case entryInfo.Mode()&os.ModeSymlink != 0:
			// 符号链接按原样复制，不跟进去：跟进去会把链接目标的内容也带进快照
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyOrLink(path, target, entryInfo.Mode(), copyFiles)
		}
	})
}

// copyOrLink 把 src 的内容落到 dst。
//
// 默认硬链接：1.3GB / 一万个文件的产物是零拷贝、毫秒级。跨文件系统（blob 在别的挂载点、
// builder 输出在另一个分区）时自动退化成复制。
//
// independent=true 表示"这两个文件必须各自独立"，也就是真复制：给可写挂载用的素材目录
// 不能和宿主共享 inode，否则容器里写一次，宿主上的原件就没了。
//
// 落盘前先删 dst：它可能是上次留下的硬链接（和 src 同一个 inode），
// 直接 O_TRUNC 打开会把 src 也清空。
func copyOrLink(src, dst string, mode os.FileMode, independent bool) error {
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !independent {
		if err := os.Link(src, dst); err == nil {
			return nil
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// CleanupSnapshots 清掉已收尾会话留下的产物快照（启动时调一次）。
//
// 正常收尾会自己删；这里兜的是"进程被 kill / 机器断电"留下的几百 MB 到几 GB 目录。
func (m *Manager) CleanupSnapshots(ctx context.Context) (int, error) {
	sessions, err := m.store.ListSessions(ctx, 0)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, sess := range sessions {
		if !sess.Status.Terminal() {
			continue
		}
		for _, sub := range []string{"payload", "assets"} {
			dir := filepath.Join(sess.Dir, sub)
			if _, err := os.Stat(dir); err == nil {
				if err := os.RemoveAll(dir); err == nil {
					removed++
				}
			}
		}
	}
	return removed, nil
}
