-- 0002_file_soft_delete.sql — files.deleted_at：删除文件软标记
--
-- 事实源：docs/knowledge_Layer/04_completeness_review_and_optimized_plan.md
-- §5 Phase 1 交付 3 —— "删除文件标记 deleted_at 而不是立即物理删除"。
-- 04 §4.3 的 files DDL 未列该列（同节交付项要求它），由本迁移补齐；这是
-- 0001_init.sql 之外的第一条增量迁移，走 internal/migrate 的版本机制。
--
-- 语义：
--   - deleted_at IS NULL：文件存在于上一次索引视图；
--   - deleted_at 非零（unix 毫秒）：文件已从磁盘消失，行与其 symbols 保留，
--     读路径（FindSymbols / Search / Stats）据此过滤；
--   - 文件重新出现时 UpsertFile 清空该列（复活），符号由 ReplaceSymbols 重建；
--   - 物理清理（GC）是 Phase 5 交付（04 §5 Phase 5 交付 4），本阶段只标记。
ALTER TABLE files ADD COLUMN deleted_at INTEGER;

CREATE INDEX idx_files_ws_deleted ON files(workspace_id, deleted_at);
