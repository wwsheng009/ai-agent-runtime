-- context_items 可解释性扩展（06 §4 Phase 6 切片 5；supplement 14 §14.2 的 v1 收口子集）。
--
-- 验收要求（04 §5 Phase 6 交付 5）：每个注入 item 必须有 source / version /
-- trust / reason / stale。0001 已有 source / trust / reason / stale；本迁移补
-- version 与配套的可解释性列：
--   version      —— 知识版本（与 ReuseItem.KnowledgeVersion / 版本观测同源）
--   confidence   —— 编译期置信度（[0,1]）
--   tier         —— hot|warm（注入条目；cold 只出现在 dropped 计数里）
--   provisional  —— 暂定标记（Provisional 复用的审计位）
--   explanation  —— 一行可解释性说明（为什么可复用 / 为什么被过滤）
--
-- 全部 additive 且带默认值：旧行读取不受影响；reader 角色只读，不写本表。

ALTER TABLE context_items ADD COLUMN version TEXT;
ALTER TABLE context_items ADD COLUMN confidence REAL NOT NULL DEFAULT 0.5;
ALTER TABLE context_items ADD COLUMN tier TEXT;
ALTER TABLE context_items ADD COLUMN provisional INTEGER NOT NULL DEFAULT 0;
ALTER TABLE context_items ADD COLUMN explanation TEXT;
