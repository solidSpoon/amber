-- 内容寻址存储：payload（应用产物）与 asset（素材/模型）都是"按 sha256 存的字节"。
--
-- 两层：
--   blobs     一个文件一份字节，跨会话共享（Electron 那 1.2G 只存一次）
--   manifests 一棵目录树（path → blob），代表一个可直接运行的产物
--
-- 引用计数决定能不能删：会话引用 manifest，manifest 引用 blob。
-- 没人引用且超过 TTL 的才回收——进程被 kill 留下的垃圾靠这个兜底。
CREATE TABLE blobs (
    digest       TEXT PRIMARY KEY,
    size         INTEGER NOT NULL,
    refs         INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    last_used_at TEXT NOT NULL
);

CREATE TABLE manifests (
    digest       TEXT PRIMARY KEY,
    entries      TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    refs         INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    last_used_at TEXT NOT NULL
);

CREATE INDEX idx_blobs_refs ON blobs (refs, last_used_at);
CREATE INDEX idx_manifests_refs ON manifests (refs, last_used_at);

-- 会话现在记"用的是哪份产物、哪些素材"，而不是"哪个仓库的哪个分支"
ALTER TABLE sessions ADD COLUMN payload TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN assets TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN recipe TEXT NOT NULL DEFAULT '';
