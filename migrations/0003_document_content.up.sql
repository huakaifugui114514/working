-- 0003_document_content：文档正文与分块
--
-- 设计约定：
--   · document_contents 一封文档一行；content_hash 是整篇正文的 SHA-256
--   · document_chunks 一行一个 chunk；chunk_index 从 0 开始，同一文档内唯一
--   · FULLTEXT 必须用 ngram parser，否则中文按空格切词，检索基本不可用
--   · 不加外键：与 0001/0002 保持一致；一致性由 repository 事务和 purge 保证
--   · 软删文档的正文/chunks 保留；purge 物理删除文档前必须先删这两张表的行

CREATE TABLE document_contents (
    document_id     BINARY(16)    NOT NULL               COMMENT '文档 id',
    content         LONGTEXT      NOT NULL               COMMENT '完整正文',
    content_hash    BINARY(32)    NOT NULL               COMMENT '正文 SHA-256',
    chunker_version INT UNSIGNED  NOT NULL               COMMENT '分块算法版本',
    created_at      DATETIME(3)   NOT NULL               COMMENT '创建时间（UTC）',
    updated_at      DATETIME(3)   NOT NULL               COMMENT '最后更新时间（UTC）',

    PRIMARY KEY (document_id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '文档正文';

CREATE TABLE document_chunks (
    id          BINARY(16)    NOT NULL               COMMENT 'chunk id（UUIDv7）',
    document_id BINARY(16)    NOT NULL               COMMENT '所属文档 id',
    chunk_index INT UNSIGNED  NOT NULL               COMMENT '文档内序号，从 0 开始',
    content     TEXT          NOT NULL               COMMENT 'chunk 正文',
    char_count  INT UNSIGNED  NOT NULL               COMMENT 'chunk 的 rune 数',
    created_at  DATETIME(3)   NOT NULL               COMMENT '创建时间（UTC）',

    PRIMARY KEY (id),
    -- 同一文档内 chunk_index 唯一；也充当按文档查 chunk 的索引前缀
    UNIQUE KEY uk_document_chunk (document_id, chunk_index),
    -- 中文检索：ngram parser；ngram_token_size 默认 2
    FULLTEXT KEY ft_chunk_content (content) WITH PARSER ngram
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '文档分块';
