package httpapi

// blobs.go：产物与素材的上传接口。
//
// 三步走，客户端只为"服务端没有的字节"付费：
//   ① POST /api/blobs/missing  {"digests":[...]}      → {"missing":[...]}
//   ② PUT  /api/blobs/{digest}  <raw bytes>           → {"digest":…,"size":…}
//   ③ POST /api/manifests       {"entries":[…]}       → {"digest":…,"bytes":…}
//
// 起会话时 payload 传 "manifest:sha256:…" 即可；同一份产物/模型只在盘上存一份。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/solidSpoon/rigd/internal/store"
)

type missingBlobsRequest struct {
	Digests []string `json:"digests"`
}

func (s *Server) missingBlobs(w http.ResponseWriter, r *http.Request) {
	var req missingBlobsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	normalized := make([]string, 0, len(req.Digests))
	for _, raw := range req.Digests {
		digest, err := store.NormalizeDigest(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		normalized = append(normalized, digest)
	}
	missing, err := s.store.MissingBlobs(r.Context(), normalized)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missing": missing})
}

// putBlob 收原始字节。digest 由客户端算好放进路径里，服务端边写边校验。
func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	digest, err := store.NormalizeDigest(chi.URLParam(r, "digest"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := int64(s.cfg.MaxUploadMB) << 20
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("文件超过上限（%d MB）", s.cfg.MaxUploadMB))
		return
	}
	got, size, err := s.store.PutBlob(r.Context(), digest, r.Body, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("上传失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"digest": got, "size": size})
}

type manifestRequest struct {
	Entries []store.ManifestEntry `json:"entries"`
}

func (s *Server) putManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	manifest, err := s.store.PutManifest(r.Context(), req.Entries)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"digest": "sha256:" + manifest.Digest,
		"bytes":  manifest.Bytes,
		"files":  len(manifest.Entries),
	})
}

// listPayloads 列出最近用过的产物与占用，供控制台选payload。
func (s *Server) listPayloads(w http.ResponseWriter, r *http.Request) {
	limited := r.URL.Query().Get("limit")
	_ = limited
	manifests, err := s.store.RecentManifests(r.Context(), 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	count, bytes, err := s.store.BlobUsage(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(manifests))
	for _, manifest := range manifests {
		out = append(out, map[string]any{
			"digest": "sha256:" + manifest.Digest,
			"bytes":  manifest.Bytes,
			"files":  len(manifest.Entries),
			"refs":   manifest.Refs,
			"usedAt": manifest.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"payloads":      out,
		"storage":       map[string]any{"blobs": count, "bytes": bytes},
		"uploadLimitMB": s.cfg.MaxUploadMB,
		"roots":         s.cfg.WorkspaceRoots,
	})
}

// 让 strings 包在本文件里保持使用（错误信息拼接用）
var _ = strings.TrimSpace
