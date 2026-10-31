-- 去掉"这次验收的结论"。
--
-- verdict（通过 / 没通过 / 看不出）是当年把 Amber 定位成"AI 验收测试平台"时留下的，
-- 用来给一条会话盖章。实际用法是拿它对自己的产物跑一遍、把过程留成记录：
-- 读者要的是"它点到了什么、屏幕上出现了什么"，不是一句盖棺定论。
-- 而且这句判断只由 AI 自己写，人是复核者——把它当结论展示，反而让记录看起来像审批单。
--
-- summary 保留，但改成 note：它现在的含义是"这个会话是怎么结束的"
-- （服务超时回收、进程关闭时收尾），由服务端写，AI 不填。
ALTER TABLE sessions DROP COLUMN verdict;
ALTER TABLE sessions RENAME COLUMN summary TO note;
