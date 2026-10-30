-- AI 用完这套工作流后留下的建议。
--
-- 这是产品改进的输入，不是会话证据：所以单独一张表，不挂在 events 里，
-- 也不出现在验收报告里（报告只讲这次验收发生了什么）。
CREATE TABLE feedback (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    session_id  TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'ai',
    rating      INTEGER NOT NULL DEFAULT 0,
    body        TEXT NOT NULL
);

CREATE INDEX idx_feedback_at ON feedback (at DESC);
