-- 会话：一次「一台可扔的测试机」的完整生命周期
CREATE TABLE sessions (
    id              TEXT PRIMARY KEY,
    app             TEXT NOT NULL,
    ref             TEXT NOT NULL DEFAULT '',
    commit_sha      TEXT NOT NULL DEFAULT '',
    image           TEXT NOT NULL,
    container       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    cdp_port        INTEGER NOT NULL DEFAULT 0,
    vnc_port        INTEGER NOT NULL DEFAULT 0,
    dir             TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    started_at      TEXT,
    ended_at        TEXT,
    idle_deadline   TEXT,
    video_path      TEXT NOT NULL DEFAULT '',
    video_duration_ms INTEGER NOT NULL DEFAULT 0,
    verdict         TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sessions_created ON sessions (created_at DESC);
CREATE INDEX idx_sessions_status ON sessions (status);

-- 事件：每一次工具调用（含失败），带录像偏移量，供前端点一下跳一段
CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    at              TEXT NOT NULL,
    tool            TEXT NOT NULL,
    args            TEXT NOT NULL DEFAULT '{}',
    ok              INTEGER NOT NULL DEFAULT 1,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    video_offset_ms INTEGER NOT NULL DEFAULT -1,
    result          TEXT NOT NULL DEFAULT '',
    artifact_path   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_events_session_seq ON events (session_id, seq);

-- 产物：截图、报告、录像等，路径相对会话目录
CREATE TABLE artifacts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    path        TEXT NOT NULL,
    bytes       INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE INDEX idx_artifacts_session ON artifacts (session_id, kind);

-- 指标采样：系统资源监控页的历史曲线
CREATE TABLE samples (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    cpu_pct     REAL NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    containers  INTEGER NOT NULL
);

CREATE INDEX idx_samples_at ON samples (at DESC);
