// Package database 负责「配置 → 数据库连接」：拼装 DSN、打开连接池、校验连通性。
//
// 为什么不放在 internal/config：config 是最底层的配置包，一旦它 import MySQL 驱动，
// 所有引用 config 的包都会被拖上驱动依赖；"怎么连数据库"本来就是 DB 层的事。
//
// 本包只做连接管理，不写业务 SQL —— 业务 SQL 属于 repository。
//
// 依赖（实现时需要，按 AGENTS.md §2.2 已获批）：github.com/go-sql-driver/mysql
package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/huakaifugui114514/working/internal/config"
)

// DSN 生成业务连接串。
//
// 契约：
//   - 用 mysql.Config + FormatDSN() 生成，禁止手拼字符串——
//     密码含 @ : / ? 时手拼必然拼坏（经典线上事故）
//   - Net="tcp"，Addr=net.JoinHostPort(host, itoa(port))（别手拼 host:port，IPv6 会错）
//   - multiStatements 必须为 false
//   - parseTime=true（否则 DATETIME 扫不进 time.Time，只能拿到 []byte）
//   - loc=UTC（约定：时间一律存 UTC，展示时再转）
//   - Timeout 设一个拨号上限（如 3s），否则连不上时会挂很久
func DSN(cfg config.Database) string {
	return buildDSN(cfg, false)
}

// MigrateDSN 生成迁移专用连接串：参数与 DSN 相同，但 multiStatements=true。
//
// 为什么迁移必须开：一个迁移文件里常有多条 DDL；golang-migrate 的官方 README
// 明确要求复用连接时带 multiStatements=true。
func MigrateDSN(cfg config.Database) string {
	return buildDSN(cfg, true)
}

// Open 打开业务连接池，并用 Ping 验证连通性。
//
// 契约：
//   - sql.Open 是懒的（不建连接），必须 PingContext 才知道配置是否正确
//   - 连接池四个参数全部来自 cfg：MaxOpenConns / MaxIdleConns /
//     ConnMaxLifetime / ConnMaxIdleTime
//   - ⚠️ DSN 含明文密码，绝不能出现在返回的 error 或日志里；
//     错误消息只带 host/port/name/user 这类可公开字段
//   - Ping 失败时要先 db.Close() 再返回 error，否则连接池泄漏
func Open(ctx context.Context, cfg config.Database, logger *slog.Logger) (*sql.DB, error) {
	// 业务连接必须用 DSN：MigrateDSN 开了 multiStatements，只给迁移用
	db, err := sql.Open("mysql", DSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接池失败（host=%s port=%d db=%s user=%s）: %w",
			cfg.Host, cfg.Port, cfg.Name, cfg.User, err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库失败（host=%s port=%d db=%s user=%s）: %w",
			cfg.Host, cfg.Port, cfg.Name, cfg.User, err)
	}

	logger.DebugContext(ctx, "open database successfully", slog.Any("database", cfg))

	return db, nil
}

// buildDSN 是两个公开构造函数共用的实现：只差 multiStatements 一个开关。
func buildDSN(cfg config.Database, allowMultiStatements bool) string {
	c := mysql.NewConfig()
	c.User, c.Passwd = cfg.User, cfg.Password
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c.DBName = cfg.Name
	c.ParseTime = true
	c.Loc = time.UTC
	c.Timeout = 3 * time.Second
	c.MultiStatements = allowMultiStatements
	return c.FormatDSN()
}
