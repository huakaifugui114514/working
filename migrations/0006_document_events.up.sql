-- 0006_document_events：Kafka 事件 outbox
--
-- 为什么不直接在 content 事务里发 Kafka：
--   外部网络调用不能和 DB 事务绑在一起；先提交事务再发会丢事件。
--   事务内写 outbox，dispatcher 异步发布，Kafka 失败可重试。
--
-- 为什么保留 embedding_jobs：
--   Kafka 是通知通道，不是任务事实源；worker 仍以 DB job 为主，
--   消费重复消息时靠 content_hash/job 状态幂等。

CREATE TABLE document_events (
    id           BINARY(16)    NOT NULL               COMMENT '事件 id（UUIDv7）',
    event_type   VARCHAR(64)   NOT NULL               COMMENT '事件类型',
    document_id  BINARY(16)    NOT NULL               COMMENT '文档 id',
    payload      JSON          NOT NULL               COMMENT '事件负载',
    status       VARCHAR(16)   NOT NULL DEFAULT 'pending' COMMENT 'pending/published/dead',
    attempts     INT UNSIGNED  NOT NULL DEFAULT 0     COMMENT '发布尝试次数',
    available_at DATETIME(3)   NOT NULL               COMMENT '下次可发布的时间',
    created_at   DATETIME(3)   NOT NULL               COMMENT '创建时间（UTC）',
    published_at DATETIME(3)   NULL                   COMMENT 'Kafka 确认时间（UTC）',

    PRIMARY KEY (id),
    KEY idx_document_events_pending (status, available_at, id),
    KEY idx_document_events_document (document_id, created_at)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = 'Kafka 事件 outbox';
