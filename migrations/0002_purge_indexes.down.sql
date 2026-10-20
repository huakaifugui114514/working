-- 0002_purge_indexes down：回滚 purge 专用索引
--
-- 只删本迁移加的索引；0001 的 idx_created_id 不动。
ALTER TABLE collections
    DROP KEY idx_deleted_id,
    ALGORITHM = INPLACE,
    LOCK = NONE;

ALTER TABLE documents
    DROP KEY idx_deleted_id,
    ALGORITHM = INPLACE,
    LOCK = NONE;
