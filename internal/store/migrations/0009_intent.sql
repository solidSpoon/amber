-- 意图：会话的标题 = 这次验收想干什么；run 的意图 = 这一段操作想干什么。
--
-- 报告里的每一段操作原来是从动作名拼出来的（「点击「Next」→ 点击「Skip」」），
-- 那是"做了什么"，不是"为什么要做"。意图由调用方给，读报告的人先看这一句就够；
-- 拼不出来的就空着，绝不编。
ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN batch_intent TEXT NOT NULL DEFAULT '';
