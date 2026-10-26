-- 0007_workflow_runs：Workflow 运行记录
--
-- 运行/节点状态落库，支持断点续跑：节点成功记录 output，
-- 重启后跳过成功节点，只重跑失败/未执行的节点。

CREATE TABLE workflow_runs (
    id            BINARY(16)   NOT NULL               COMMENT 'run id（UUIDv7）',
    workflow_name VARCHAR(128) NOT NULL               COMMENT 'YAML 里的 name',
    status        VARCHAR(16)  NOT NULL               COMMENT 'pending/running/succeeded/failed',
    input         JSON         NULL                   COMMENT '运行输入',
    output        JSON         NULL                   COMMENT '节点输出快照',
    last_error    TEXT         NULL                   COMMENT '最终错误',
    created_at    DATETIME(3)  NOT NULL               COMMENT '创建时间（UTC）',
    started_at    DATETIME(3)  NULL                   COMMENT '开始时间（UTC）',
    finished_at   DATETIME(3)  NULL                   COMMENT '结束时间（UTC）',

    PRIMARY KEY (id),
    KEY idx_workflow_runs_name_created (workflow_name, created_at)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = 'Workflow 运行记录';

CREATE TABLE workflow_node_runs (
    run_id     BINARY(16)   NOT NULL               COMMENT 'workflow_runs.id',
    node_id    VARCHAR(64)  NOT NULL               COMMENT 'YAML 节点 id',
    status     VARCHAR(16)  NOT NULL               COMMENT 'pending/running/succeeded/failed/skipped',
    attempts   INT UNSIGNED NOT NULL DEFAULT 0     COMMENT '尝试次数',
    output     JSON         NULL                   COMMENT '节点输出',
    last_error TEXT         NULL                   COMMENT '最近错误',
    started_at DATETIME(3)  NULL                   COMMENT '开始时间（UTC）',
    finished_at DATETIME(3) NULL                   COMMENT '结束时间（UTC）',

    PRIMARY KEY (run_id, node_id)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci
  COMMENT = 'Workflow 节点运行记录';
