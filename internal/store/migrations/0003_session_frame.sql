-- 画面几何：录像里真正有内容的那块区域，以及它所在的虚拟屏尺寸。
--
-- 录屏抓的是虚拟屏（这样弹窗、菜单、原生对话框都不会漏），但应用窗口没铺满时，
-- 整段录像就是「角落里一个小窗 + 一大片空桌面」。控制台拿到这两组数字之后，
-- 才能按窗口把画面裁出来显示，并在界面上说清「这段录像的有效区域是哪儿」。
--
-- 0 表示未知（旧版本录的像），读取时会从录像里量一次并回填。
ALTER TABLE sessions ADD COLUMN frame_x INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN frame_y INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN frame_w INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN frame_h INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN display_w INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN display_h INTEGER NOT NULL DEFAULT 0;
