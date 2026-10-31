-- 会话上两个"只写不读"的溯源列。
--
--   sessions.image  镜像名：真正的值在配置里（RIGD_IMAGE），会话这边存了一份却没有任何
--                   地方读它。要证明"这次用的是哪个镜像"，应该是界面上的一个字段，
--                   而不是数据库里没人看的一列。
--   sessions.recipe 产物自述 rig.json 的原文快照：同样没有读取方。产物本身留在
--                   内容寻址库里（payload/manifest 的 digest 记在 sessions.payload），
--                   想知道当时怎么启动的，看那份产物比看这份快照更准。
--
-- image 是 NOT NULL 列，DROP COLUMN 在 sqlite 里对带约束的列会重建表，这里直接重建，
-- 顺便把 tab 里的默认值一起清掉（迁移顺序保证前面的迁移都已执行）。
ALTER TABLE sessions DROP COLUMN image;
ALTER TABLE sessions DROP COLUMN recipe;
