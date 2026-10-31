-- 录像真正开始的墙钟毫秒。
--
-- 每一步的 video_offset_ms 都是"相对录像起点"的偏移（这一点一直是对的：容器里的
-- .recording.json 记着 ffmpeg 真正开录的时刻，操作偏移量按它算）。但起点本身只活在
-- 内存里，会话一收尾就没了——于是时间线里的 offsetMs 只是个"第几毫秒"，
-- 对不上任何墙钟时间，也就没法和别的日志（应用日志、CI、聊天记录）对齐。
--
-- 0 表示不知道（老会话、或者容器没写出 .recording.json）。
ALTER TABLE sessions ADD COLUMN recording_start_ms INTEGER NOT NULL DEFAULT 0;
