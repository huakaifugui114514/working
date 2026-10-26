-- 0005_vector_cleanup_jobs：purge 物理删除文档后，异步清理 Qdrant 向量
--
-- 为什么不在 purge 的 MySQL 事务里调 Qdrant：
--   外部调用可能慢/失败，不能把 DB 事务拖住；也不能在 DB 已提交后丢失“要删哪些向量”的信息。
--   purge 事务里写 outbox，cleanup worker 异步删除，失败可重试。
--
-- 为什么 document_id 做主键：
--   同一文档重复 purge 只保留一条任务；Qdrant 删除本身是幂等的。

CREATE TABLE vector_cleanup_jobs (
    document_id BINARY(16)    NOT NULL               COMMENT '已物理删除的文档 id',
    status      VARCHAR(16)   NOT NULL               COMMENT 'pending/running/done/failed/dead',
    attempts    INT UNSIGNED  NOT NULL DEFAULT 0     COMMENT '已尝试次数',
    available_at DATETIME(3)  NOT NULL               COMMENT '下次可被抢占的时间',
    locked_at   DATETIME(3)   NULL                   COMMENT 'worker lease 开始时间',
    locked_by   VARCHAR(128)  NULL                   COMMENT 'worker 标识',
    last_error  VARCHAR(1024) NULL                   COMMENT '最近一次失败原因（截断）',
    created_at  DATETIME(3)   NOT NULL               COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3)   NOT NULL               COMMENT '最后更新时间（UTC）',

    PRIMARY KEY (document_id),
    KEY idx_vector_cleanup_claim (status, available_at),
    KEY idx_vector_cleanup_lease (status, locked_at)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = 'Qdrant 向量清理 outbox';
