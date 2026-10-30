-- 报告层：让每条审计事件都能独立成为报告里的一步。
--
-- caption 是动作的「人话」描述（点击「保存」按钮），自动从可访问性树生成；
-- intent/expect 是调用方补的「为什么做」「期望看到什么」，可空。
-- shot 是这一步的画面（自动截，失败也截）。kind 区分动作、断言、说明。
ALTER TABLE events ADD COLUMN kind TEXT NOT NULL DEFAULT 'action';
ALTER TABLE events ADD COLUMN caption TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN intent TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN expect TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN shot TEXT NOT NULL DEFAULT '';
