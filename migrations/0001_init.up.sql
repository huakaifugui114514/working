-- 0001_init：初始 schema —— collections（知识库）
--
-- 设计约定见 AGENTS.md §7「M2 数据模型决策」：
--   · 主键 BINARY(16)：存 UUIDv7 的 16 字节。比 CHAR(36) 省一半以上的索引空间；
--     且大表上 CHAR→BINARY 是"重建表 + 全部索引"的不可逆 DDL，所以一步到位。
--   · UUIDv7 的二进制序就是时间序（实测），插入接近顺序写，缓解 InnoDB 页分裂。
--   · 时间列 DATETIME(3)：毫秒精度，存 UTC（应用层负责转换，服务器时区已固定 +00:00）。
--   · 软删：deleted_at IS NULL 表示未删除。刻意不建唯一约束——否则"软删后同名重建"会撞唯一索引。
--   · 分页走 keyset：游标是 (created_at, id)，所以索引必须是 (created_at, id) 这个顺序。
--   · 不加外键（见决策记录）：写入放大、跨分片不可行、大表 DDL 灾难。
--
-- 注意：这里刻意【不】写 IF NOT EXISTS —— 迁移应当"表意外存在就大声失败"，
-- 而不是静默跳过，否则 schema 会悄悄漂移，版本号却继续前进。

CREATE TABLE collections (
    id          BINARY(16)   NOT NULL               COMMENT 'UUIDv7 的 16 字节',
    name        VARCHAR(255) NOT NULL               COMMENT '知识库名称',
    description VARCHAR(1024)    NULL               COMMENT '描述',
    created_at  DATETIME(3)  NOT NULL               COMMENT '创建时间（UTC）',
    updated_at  DATETIME(3)  NOT NULL               COMMENT '最后更新时间（UTC），由应用写入',
    deleted_at  DATETIME(3)      NULL DEFAULT NULL  COMMENT '软删时间（UTC）；NULL = 未删除',

    PRIMARY KEY (id),
    -- keyset 分页：WHERE deleted_at IS NULL ORDER BY created_at DESC, id DESC
    -- 把 deleted_at 放最左没有意义（几乎全是 NULL，选择性极低）。
    KEY idx_created_id (created_at, id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = '知识库';
