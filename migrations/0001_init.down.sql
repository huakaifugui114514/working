-- 回滚 0001_init（按建表的逆序删）。
-- 只用于本地重置/回滚；生产执行前务必确认数据已备份（这是删表）。
DROP TABLE IF EXISTS collection_documents;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS collections;
