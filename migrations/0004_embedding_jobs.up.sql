-- 0004_embedding_jobs：正文变更 → embedding 任务 outbox
--
-- 为什么和正文替换放在同一个事务：
--   外部 embedding 调用不能进 HTTP 事务，但“正文变了，需要重建向量”这件事必须
--   和正文提交原子发生。否则会出现正文已更新、任务却没入队的静默不一致。
--
-- 为什么一篇文章一行：
--   ReplaceDocumentContent 会删掉旧 chunks、生成全新 chunk id；按 document_id
--   重建是最简单的幂等模型。连续 PUT 只保留最新 content_hash。
--
-- 不加外键：与 0001~0003 保持一致；purge 负责物理删除 job。

CREATE TABLE embedding_jobs (
    document_id     BINARY(16)    NOT NULL               COMMENT '文档 id',
    content_hash    BINARY(32)    NOT NULL               COMMENT '触发本次任务的正文 SHA-256',
    chunker_version INT UNSIGNED  NOT NULL               COMMENT '分块算法版本',
    status          VARCHAR(16)   NOT NULL               COMMENT 'pending/running/done/failed/dead',
    attempts        INT UNSIGNED  NOT NULL DEFAULT 0     COMMENT '已尝试次数',
    available_at    DATETIME(3)   NOT NULL               COMMENT '下次可被抢占的时间',
    locked_at       DATETIME(3)   NULL                   COMMENT 'worker lease 开始时间',
    locked_by       VARCHAR(128)  NULL                   COMMENT 'worker 标识',
    last_error      VARCHAR(1024) NULL                   COMMENT '最近一次失败原因（截断）',
    created_at      DATETIME(3)   NOT NULL               COMMENT '创建时间（UTC）',
    updated_at      DATETIME(3)   NOT NULL               COMMENT '最后更新时间（UTC）',

    PRIMARY KEY (document_id),
    KEY idx_embedding_jobs_claim (status, available_at),
    KEY idx_embedding_jobs_lease (status, locked_at)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '正文 embedding outbox';

-- 历史正文补任务：迁移前已经存在的 document_contents 也要被向量化。
-- 新正文由 ReplaceDocumentContent 在同一个事务里 upsert，不依赖这条 SQL。
INSERT INTO embedding_jobs
    (document_id, content_hash, chunker_version, status, attempts,
     available_at, created_at, updated_at)
SELECT
    document_id, content_hash, chunker_version, 'pending', 0,
    UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)
FROM document_contents;
