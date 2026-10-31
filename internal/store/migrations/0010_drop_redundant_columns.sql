-- 数据库里三处「同一个事实存了两份」的清理。
--
--   artifacts 表：从来没有写入方。步骤图记在 events.shot，片段按批次命名放在
--                clips/ 下，产物索引是空壳——留着它只会让人以为"产物登记在这"。
--   sessions.commit_sha：和 ref 一直写同一个值（会话创建时两者都来自 options.Commit），
--                界面、MCP 返回、看板读的都是 ref。留两列就要保证两边同步。
--   sessions.video_path：恒等于 <会话目录>/video/session.mp4（容器里 RIG_OUT=/out 约定的位置）。
DROP TABLE IF EXISTS artifacts;

ALTER TABLE sessions DROP COLUMN commit_sha;
ALTER TABLE sessions DROP COLUMN video_path;
