-- 一段连续操作（一次 run 调用）的起止时间。
--
-- 切片按"这一段"算，不按"单步"算：一批里的相邻两步可能只隔 0.2 秒，
-- 每步各切前后 2 秒会把同一段画面重复十几遍。一批就是一条连续录像：
-- 从这批第一步前 2 秒，到最后一步后 2 秒。
ALTER TABLE events ADD COLUMN batch_start_ms INTEGER NOT NULL DEFAULT -1;
ALTER TABLE events ADD COLUMN batch_end_ms INTEGER NOT NULL DEFAULT -1;
