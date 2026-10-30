package store

// blobs.go：内容寻址存储（CAS）。
//
// 为什么不用"每次上传一个 tar 包"：一个 Electron 打包目录 1.3GB，其中真正每次变的是
// app.asar（几十 MB）。按文件寻址之后，客户端先问「这些 digest 你有没有」，只传缺的，
// 迭代时几乎零传输；同一份模型/素材跨会话也只在盘上存一份。
//
// 文件落在 <data>/blobs/sha256/<前两位>/<其余>，与数据库里的引用计数配合回收。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrBlobNotFound 表示这个 digest 的字节不在库里。
var ErrBlobNotFound = errors.New("blob 不存在")

// ManifestEntry 目录树里的一项。
type ManifestEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"` // 裸 hex（sha256）
	Size   int64  `json:"size"`
	// Mode 可执行位等权限；0 表示默认 0644（可执行文件 0755）
	Mode uint32 `json:"mode,omitempty"`
}

// Manifest 一棵目录树 = 一个可以直接运行的产物。
type Manifest struct {
	Digest string
	// LastUsedAt 最近一次被引用的时间（RFC3339），展示用
	LastUsedAt string
	// Entries 按 path 排序，digest 就是这份列表的 sha256（同样的树 → 同样的 digest）
	Entries []ManifestEntry
	Bytes   int64
	Refs    int
}

// NormalizeDigest 接受 "sha256:abc…" 或裸 hex，统一成裸小写 hex。
func NormalizeDigest(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	d = strings.TrimPrefix(d, "sha256:")
	d = strings.ToLower(d)
	if len(d) != 64 {
		return "", fmt.Errorf("digest 长度不对（应为 64 位 hex）：%q", raw)
	}
	if _, err := hex.DecodeString(d); err != nil {
		return "", fmt.Errorf("digest 不是合法 hex：%q", raw)
	}
	return d, nil
}

// BlobPath 字节在磁盘上的位置。
func (s *Store) BlobPath(digest string) string {
	return filepath.Join(s.blobRoot, "sha256", digest[:2], digest[2:])
}

// HasBlob 判断字节是否已在库里。
func (s *Store) HasBlob(ctx context.Context, digest string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM blobs WHERE digest = ?`, digest).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// MissingBlobs 返回客户端还需要上传的那些（上传前先问，避免白传 1.2G）。
func (s *Store) MissingBlobs(ctx context.Context, digests []string) ([]string, error) {
	missing := make([]string, 0, len(digests))
	for _, digest := range digests {
		ok, err := s.HasBlob(ctx, digest)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, digest)
		}
	}
	return missing, nil
}

// PutBlob 边写边算 sha256，写完校验调用方声明的 digest 是否一致。
//
// 不一致直接报错并删掉临时文件：宁可让上传失败，也不能把"内容和名字对不上"的字节收进库。
func (s *Store) PutBlob(ctx context.Context, expect string, body io.Reader, limit int64) (string, int64, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.blobRoot, "tmp"), "upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	hasher := sha256.New()
	limited := io.LimitReader(body, limit+1)
	size, copyErr := io.Copy(io.MultiWriter(tmp, hasher), limited)
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("写入上传内容失败: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("落盘失败: %w", closeErr)
	}
	if size > limit {
		return "", 0, fmt.Errorf("文件超过单文件上限（%d 字节）", limit)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if expect != "" && expect != digest {
		return "", 0, fmt.Errorf("digest 不匹配：声明 %s，实际 %s", expect, digest)
	}

	path := s.BlobPath(digest)
	if _, err := os.Stat(path); err == nil {
		// 已经有了：同样的字节不重复占盘
		_ = s.touchBlob(ctx, digest)
		return digest, size, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, fmt.Errorf("创建 blob 目录失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", 0, fmt.Errorf("写入 blob 失败: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO blobs (digest, size, refs, created_at, last_used_at) VALUES (?, ?, 0, ?, ?)
		 ON CONFLICT(digest) DO UPDATE SET last_used_at = excluded.last_used_at`,
		digest, size, now, now); err != nil {
		return "", 0, fmt.Errorf("登记 blob 失败: %w", err)
	}
	return digest, size, nil
}

func (s *Store) touchBlob(ctx context.Context, digest string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE blobs SET last_used_at = ? WHERE digest = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), digest)
	return err
}

// BlobFile 打开一个 blob 供读取（物化目录树时用）。
func (s *Store) BlobFile(digest string) (*os.File, error) {
	return os.Open(s.BlobPath(digest))
}

// PutManifest 登记一棵目录树；已存在则复用（同样的树 → 同样的 digest）。
func (s *Store) PutManifest(ctx context.Context, entries []ManifestEntry) (Manifest, error) {
	if len(entries) == 0 {
		return Manifest{}, errors.New("manifest 不能为空")
	}
	clean := make([]ManifestEntry, 0, len(entries))
	for _, entry := range entries {
		path, err := cleanRelPath(entry.Path)
		if err != nil {
			return Manifest{}, err
		}
		digest, err := NormalizeDigest(entry.Digest)
		if err != nil {
			return Manifest{}, err
		}
		clean = append(clean, ManifestEntry{Path: path, Digest: digest, Size: entry.Size, Mode: entry.Mode})
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].Path < clean[j].Path })

	canonical, err := json.Marshal(clean)
	if err != nil {
		return Manifest{}, err
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])

	var total int64
	for _, entry := range clean {
		ok, err := s.HasBlob(ctx, entry.Digest)
		if err != nil {
			return Manifest{}, err
		}
		if !ok {
			return Manifest{}, fmt.Errorf("manifest 引用了不存在的 blob：%s", entry.Digest)
		}
		total += entry.Size
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM manifests WHERE digest = ?`, digest).Scan(&exists); err == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE manifests SET last_used_at = ? WHERE digest = ?`, now, digest); err != nil {
			return Manifest{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO manifests (digest, entries, bytes, refs, created_at, last_used_at)
			 VALUES (?, ?, ?, 0, ?, ?)`, digest, string(canonical), total, now, now); err != nil {
			return Manifest{}, err
		}
		// 树引用了这些字节：blob 的引用计数 +1，manifest 被回收时再还回去
		for _, entry := range clean {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refs = refs + 1 WHERE digest = ?`, entry.Digest); err != nil {
				return Manifest{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Manifest{}, err
	}
	return Manifest{Digest: digest, Entries: clean, Bytes: total}, nil
}

// GetManifest 读一棵目录树。
func (s *Store) GetManifest(ctx context.Context, digest string) (Manifest, error) {
	var (
		raw   string
		bytes int64
		refs  int
	)
	err := s.db.QueryRowContext(ctx, `SELECT entries, bytes, refs FROM manifests WHERE digest = ?`, digest).
		Scan(&raw, &bytes, &refs)
	if errors.Is(err, sql.ErrNoRows) {
		return Manifest{}, fmt.Errorf("manifest %s 不存在", digest)
	}
	if err != nil {
		return Manifest{}, err
	}
	var entries []ManifestEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return Manifest{}, fmt.Errorf("解析 manifest 失败: %w", err)
	}
	return Manifest{Digest: digest, Entries: entries, Bytes: bytes, Refs: refs}, nil
}

// Retain 把一个 manifest 或 blob 标记为"被用着"（起会话时调用）。
func (s *Store) Retain(ctx context.Context, kind, digest string) error {
	table := "manifests"
	if kind == "blob" {
		table = "blobs"
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+table+` SET refs = refs + 1, last_used_at = ? WHERE digest = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), digest)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s %s 不存在", kind, digest)
	}
	return nil
}

// Release 归还引用（会话收尾时调用）。
func (s *Store) Release(ctx context.Context, kind, digest string) error {
	table := "manifests"
	if kind == "blob" {
		table = "blobs"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE `+table+` SET refs = CASE WHEN refs > 0 THEN refs - 1 ELSE 0 END where digest = ?`, digest)
	return err
}

// Sweep 回收没人引用且超过 ttl 的 manifest 与 blob，返回删掉的字节数。
//
// 这是"结束即删"的兜底：进程被 kill、上传传一半、客户端没调收尾，都会留下没人引用的垃圾。
func (s *Store) Sweep(ctx context.Context, ttl time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339Nano)

	rows, err := s.db.QueryContext(ctx,
		`SELECT digest, entries FROM manifests WHERE refs <= 0 AND last_used_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type dead struct {
		digest  string
		entries []ManifestEntry
	}
	var deadManifests []dead
	for rows.Next() {
		var digest, raw string
		if err := rows.Scan(&digest, &raw); err != nil {
			_ = rows.Close()
			return 0, err
		}
		var entries []ManifestEntry
		_ = json.Unmarshal([]byte(raw), &entries)
		deadManifests = append(deadManifests, dead{digest: digest, entries: entries})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	var freed int64
	for _, item := range deadManifests {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return freed, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM manifests WHERE digest = ? AND refs <= 0`, item.digest); err != nil {
			_ = tx.Rollback()
			return freed, err
		}
		for _, entry := range item.entries {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refs = CASE WHEN refs > 0 THEN refs - 1 ELSE 0 END WHERE digest = ?`,
				entry.Digest); err != nil {
				_ = tx.Rollback()
				return freed, err
			}
		}
		if err := tx.Commit(); err != nil {
			return freed, err
		}
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT digest, size FROM blobs WHERE refs <= 0 AND last_used_at < ?`, cutoff)
	if err != nil {
		return freed, err
	}
	type deadBlob struct {
		digest string
		size   int64
	}
	var deadBlobs []deadBlob
	for rows.Next() {
		var item deadBlob
		if err := rows.Scan(&item.digest, &item.size); err != nil {
			_ = rows.Close()
			return freed, err
		}
		deadBlobs = append(deadBlobs, item)
	}
	_ = rows.Close()

	for _, item := range deadBlobs {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM blobs WHERE digest = ? AND refs <= 0`, item.digest); err != nil {
			return freed, err
		}
		if err := os.Remove(s.BlobPath(item.digest)); err == nil {
			freed += item.size
		}
	}
	return freed, nil
}

// RecentManifests 最近用过的产物（控制台选 payload 用）。
func (s *Store) RecentManifests(ctx context.Context, limit int) ([]Manifest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT digest, entries, bytes, refs, last_used_at FROM manifests ORDER BY last_used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Manifest, 0, limit)
	for rows.Next() {
		var (
			manifest   Manifest
			raw        string
			lastUsedAt string
		)
		if err := rows.Scan(&manifest.Digest, &raw, &manifest.Bytes, &manifest.Refs, &lastUsedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &manifest.Entries)
		manifest.LastUsedAt = lastUsedAt
		out = append(out, manifest)
	}
	return out, rows.Err()
}

// BlobUsage 库里一共占用多少（看板显示用）。
func (s *Store) BlobUsage(ctx context.Context) (count int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(1), COALESCE(SUM(size), 0) FROM blobs`).Scan(&count, &bytes)
	return count, bytes, err
}

// cleanRelPath 校验 manifest 里的路径：必须是相对的、不能跳出根目录。
func cleanRelPath(raw string) (string, error) {
	p := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(raw)), "./")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\x00") {
		return "", fmt.Errorf("manifest 路径非法：%q", raw)
	}
	return p, nil
}
