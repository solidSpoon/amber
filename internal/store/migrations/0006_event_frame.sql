-- 每一步当时的画面几何。
--
-- 窗口会在会话中途变（应用登录后切全屏、自己缩回默认尺寸），所以"整场一个 frame"
-- 是错的：用最后的几何去裁前面的片段，会裁成「窗口 + 一圈黑边」。
-- 记在事件上，每步都能按它自己那一刻的窗口裁剪。
ALTER TABLE events ADD COLUMN frame_x INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN frame_y INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN frame_w INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN frame_h INTEGER NOT NULL DEFAULT 0;
