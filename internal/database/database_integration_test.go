//go:build integration

package database

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huakaifugui114514/working/internal/config"
)

// 集成测试：需要本地 MySQL（deploy/docker-compose.yml）。
// 默认不跑（make check 不连库）；手动运行：
//
//	set -a; . deploy/.env; set +a
//	KF_DATABASE_HOST=127.0.0.1 KF_DATABASE_PORT=$MYSQL_PORT \
//	KF_DATABASE_NAME=$MYSQL_DATABASE KF_DATABASE_USER=$MYSQL_USER \
//	KF_DATABASE_PASSWORD=$MYSQL_PASSWORD \
//		go test -tags=integration -run Integration -v ./internal/database/
func TestIntegrationOpen(t *testing.T) {
	cfg, err := config.Load("") // 默认值 + 环境变量
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	ctx := context.Background()

	db, err := Open(ctx, cfg.Database, discardLogger())
	if err != nil {
		t.Fatalf("连接本地 MySQL 失败（先 cd deploy && docker compose up -d）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// ① 连接池参数确实生效
	if got := db.Stats().MaxOpenConnections; got != cfg.Database.MaxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, cfg.Database.MaxOpenConns)
	}

	// ② 业务连接必须拒绝多语句。
	//    这是"Open 误用 MigrateDSN"的回归防线：真开错了，这条会红。
	if _, err := db.ExecContext(ctx, "SELECT 1; SELECT 2"); err == nil {
		t.Error("业务连接竟然允许一次执行多条语句 —— multiStatements 被打开了，" +
			"检查 Open 是否误用了 MigrateDSN")
	}

	// ③ parseTime=true：DATETIME 能扫进 time.Time（而不是只能拿到 []byte）
	var now time.Time
	if err := db.QueryRowContext(ctx, "SELECT NOW()").Scan(&now); err != nil {
		t.Errorf("DATETIME 应能扫进 time.Time（parseTime 没生效？）: %v", err)
	}
	if now.IsZero() {
		t.Error("扫出来的时间不应为零值")
	}

	// ④ 迁移连接应当允许多语句（与 ② 形成对照，证明两条串确实是分开的）
	migrateDB, err := sql.Open("mysql", MigrateDSN(cfg.Database))
	if err != nil {
		t.Fatalf("打开迁移连接失败: %v", err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })
	if _, err := migrateDB.ExecContext(ctx, "SELECT 1; SELECT 2"); err != nil {
		t.Errorf("迁移连接应当允许多语句: %v", err)
	}
}
