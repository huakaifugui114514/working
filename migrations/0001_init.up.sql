-- 0001_init：初始 schema —— 知识库 / 文档 / 多对多关联
--
-- 设计约定见 AGENTS.md §7「M2 数据模型决策」：
--   · 主键 BINARY(16)：存 UUIDv7 的 16 字节。比 CHAR(36) 省一半以上的索引空间；
--     且大表上 CHAR→BINARY 是"重建表 + 全部索引"的不可逆 DDL，所以一步到位。
--   · UUIDv7 的二进制序就是时间序（实测），插入接近顺序写，缓解 InnoDB 页分裂。
--   · 时间列 DATETIME(3)：毫秒精度，存 UTC（应用层转换；服务器时区已固定 +00:00）。
--   · 软删：deleted_at IS NULL 表示未删除。刻意不建唯一约束——否则"软删后同名重建"会撞唯一索引。
--   · 分页走 keyset：游标是 (created_at, 并列键)，所以索引顺序必须与 ORDER BY 一致。
--   · 不加外键（见决策记录）：写入放大、跨分片不可行、大表 DDL 灾难；一致性靠 repository + 对账。
--
-- 注意：刻意【不】写 IF NOT EXISTS —— 迁移应当"表意外存在就大声失败"，
-- 而不是静默跳过，否则 schema 会悄悄漂移，版本号却继续前进。

-- 知识库
CREATE TABLE collections (
    id          BINARY(16)   NOT NULL               COMMENT 'UUIDv7 的 16 字节',
    name        VARCHAR(255) NOT NULL               COMMENT '知识库名称',
    description VARCHAR(1024)    NULL               COMMENT '描述',
    created_at  DATETIME(3)  NOT NULL               COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3)  NOT NULL               COMMENT '最后更新时间（UTC），由应用写入',
    deleted_at  DATETIME(3)      NULL DEFAULT NULL  COMMENT '软删时间（UTC）；NULL = 未删除',

    PRIMARY KEY (id),
    -- keyset 分页：WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC
    -- deleted_at 不放最左：几乎全是 NULL，选择性极低。
    KEY idx_created_id (created_at, id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '知识库';

-- 文档（M2 只存元数据；正文/分块留给 M3）
CREATE TABLE documents (
    id          BINARY(16)      NOT NULL               COMMENT 'UUIDv7 的 16 字节',
    title       VARCHAR(512)    NOT NULL               COMMENT '文档标题',
    source_uri  VARCHAR(2048)       NULL               COMMENT '来源（文件路径 / URL）；不建索引',
    mime_type   VARCHAR(128)        NULL               COMMENT 'MIME 类型，如 application/pdf',
    size_bytes  BIGINT UNSIGNED     NULL               COMMENT '字节数；UNSIGNED 防负数',
    created_at  DATETIME(3)     NOT NULL               COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3)     NOT NULL               COMMENT '最后更新时间（UTC），由应用写入',
    deleted_at  DATETIME(3)         NULL DEFAULT NULL  COMMENT '软删时间（UTC）；NULL = 未删除',

    PRIMARY KEY (id),
    KEY idx_created_id (created_at, id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '文档元数据';

-- 知识库 ↔ 文档 多对多关联。
-- 用联合主键而不是自增 id：既天然防重复关联，又直接充当"按知识库查"的索引前缀。
CREATE TABLE collection_documents (
    collection_id BINARY(16)  NOT NULL              COMMENT '知识库 id',
    document_id   BINARY(16)  NOT NULL              COMMENT '文档 id',
    created_at    DATETIME(3) NOT NULL              COMMENT '加入时间（UTC）；列表按它倒序',

    PRIMARY KEY (collection_id, document_id),
    -- 反向查："这篇文档属于哪些知识库"；文档被删时也要按它清理关联
    KEY idx_document (document_id),
    -- keyset 分页：WHERE collection_id = ? AND (created_at < ? OR (created_at = ? AND document_id < ?))
    -- ORDER BY created_at DESC, document_id DESC
    -- 注意：这里没有独立的 id 列，并列键用 document_id。
    KEY idx_collection_created (collection_id, created_at, document_id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '知识库-文档 关联';
