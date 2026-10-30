-- 会话占用的"机器槽位"：看板上固定两块屏，0/1 两个槽位对应两台候选机器。
-- 槽位是调度资源，和会话一一绑定，回收后释放。
ALTER TABLE sessions ADD COLUMN slot INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_sessions_slot ON sessions (slot, status);
