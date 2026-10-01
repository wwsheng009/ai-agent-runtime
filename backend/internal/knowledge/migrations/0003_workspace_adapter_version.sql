-- Phase 4 交付 4：adapter 版本参与符号身份（signature_hash / stable_key）。
-- 记录"上一次成功索引使用的 adapter 版本"；与当前 adapter 版本不一致时，
-- RunIndex 按 knowledge.index.full_rebuild_on_adapter_change 触发全量重建。
ALTER TABLE workspaces ADD COLUMN adapter_version TEXT NOT NULL DEFAULT '';
