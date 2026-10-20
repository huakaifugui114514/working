-- 0002_purge_indexes：为软删清理补 (deleted_at, id) 索引
--
-- 背景：purge 按 `deleted_at IS NOT NULL AND deleted_at < cutoff ORDER BY deleted_at, id`
-- 扫描过期行。0001 只有 (created_at, id)，无法高效定位软删数据；全表扫在大表上
-- 会拖垮主从延迟。
--
-- 为什么是 (deleted_at, id)：
--   · deleted_at 放最左，WHERE 是范围条件，能直接走 range；
--   · id 放第二列，让 ORDER BY deleted_at, id LIMIT n 走索引顺序，避免 filesort；
--   · deleted_at 可空但清理只查 IS NOT NULL；id 唯一，索引长度可控。
--
-- 0001 已应用，不能回头改；这里只追加。生产上加二级索引用 INPLACE + LOCK=NONE，
-- 避免阻塞读写（MySQL 8 支持；不支持时会大声失败，而不是悄悄锁表）。
ALTER TABLE collections
    ADD KEY idx_deleted_id (deleted_at, id),
    ALGORITHM = INPLACE,
    LOCK = NONE;

ALTER TABLE documents
    ADD KEY idx_deleted_id (deleted_at, id),
    ALGORITHM = INPLACE,
    LOCK = NONE;
