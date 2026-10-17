// Command migrate 执行数据库迁移（golang-migrate + 编译进二进制的 migrations/）。
//
// 用法：
//
//	migrate up               应用所有未执行的迁移
//	migrate version          显示当前版本与 dirty 状态
//	migrate down -y          （危险）回滚一步
//	migrate force <version>  修复 dirty：把版本表标记为 <version>（不执行 SQL）
//
// 为什么是独立命令，而不是 apid 启动时自动迁移：
//   - 多副本同时启动会并发迁移（migrate 有 GET_LOCK，但"一个副本迁移、其他副本等"更糟）
//   - 迁移应该在部署流水线里是一个独立、可观察、可重试的步骤
//   - 服务启动卡在迁移上，会把"启动失败"和"迁移失败"两件事混在一起
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	migrate "github.com/golang-migrate/migrate/v4"
	mysqldrv "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
	"github.com/huakaifugui114514/working/internal/observability"
	"github.com/huakaifugui114514/working/migrations"
)

const usage = `用法：
  migrate up              应用所有未执行的迁移
  migrate version         显示当前版本与 dirty 状态
  migrate down -y         （危险）回滚一步
  migrate force <version> 修复 dirty：把版本表标记为 <version>（不执行 SQL）`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "migrate 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}

	switch args[0] {
	case "up":
		return withMigrator(func(m *migrate.Migrate, logger *slog.Logger) error {
			logger.Info("开始执行迁移")
			err := m.Up()
			if errors.Is(err, migrate.ErrNoChange) {
				logger.Info("数据库已是最新版本，无需迁移")
				return nil
			}
			if err != nil {
				return fmt.Errorf("执行迁移失败（若已 dirty，用 migrate force <上一个成功版本> 修复）: %w", err)
			}
			version, dirty, err := m.Version()
			if err != nil {
				return fmt.Errorf("迁移已执行，但读取版本失败: %w", err)
			}
			logger.Info("迁移完成", "version", version, "dirty", dirty)
			return nil
		})

	case "version":
		return withMigrator(func(m *migrate.Migrate, logger *slog.Logger) error {
			version, dirty, err := m.Version()
			if errors.Is(err, migrate.ErrNilVersion) {
				logger.Info("尚未应用任何迁移")
				return nil
			}
			if err != nil {
				return fmt.Errorf("读取版本失败: %w", err)
			}
			logger.Info("当前版本", "version", version, "dirty", dirty)
			if dirty {
				// dirty = 上次迁移执行到一半失败，版本表被标记住，后续 up 会被拒绝。
				// 必须人工确认数据状态后再 force，不能自动修。
				return errors.New("数据库处于 dirty 状态（上次迁移中途失败）——确认数据后执行 migrate force <上一个成功版本>")
			}
			return nil
		})

	case "down":
		if len(args) < 2 || args[1] != "-y" {
			return errors.New("down 会回滚一步并删改数据；确认请执行: migrate down -y")
		}
		return withMigrator(func(m *migrate.Migrate, logger *slog.Logger) error {
			if err := m.Steps(-1); err != nil {
				if errors.Is(err, migrate.ErrNoChange) {
					logger.Info("没有可回滚的迁移")
					return nil
				}
				return fmt.Errorf("回滚失败: %w", err)
			}
			logger.Info("已回滚一步")
			return nil
		})

	case "force":
		if len(args) < 2 {
			return errors.New("用法: migrate force <version>")
		}
		version, err := strconv.ParseUint(args[1], 10, 32)
		if err != nil {
			return fmt.Errorf("版本号必须是正整数: %w", err)
		}
		return withMigrator(func(m *migrate.Migrate, logger *slog.Logger) error {
			if err := m.Force(int(version)); err != nil {
				return fmt.Errorf("force 失败: %w", err)
			}
			logger.Warn("已强制标记版本（未执行任何 SQL，只改了版本表）", "version", version)
			return nil
		})

	default:
		return fmt.Errorf("未知子命令 %q\n%s", args[0], usage)
	}
}

// withMigrator 负责"配置 → logger → 迁移连接 → migrate 实例"这套样板，并在结束时收尾。
func withMigrator(fn func(*migrate.Migrate, *slog.Logger) error) error {
	path := os.Getenv(config.EnvConfigFile)
	if path == "" {
		path = config.DefaultPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("加载配置失败（path=%s）: %w", path, err)
	}

	logger, err := observability.NewLogger(string(cfg.Log.Level), string(cfg.Log.Format), os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化 logger 失败: %w", err)
	}
	slog.SetDefault(logger)

	// 迁移连接必须开 multiStatements（一个迁移文件里常有多条 DDL）。
	// 这里直接 sql.Open + MigrateDSN，而不是 database.Open：后者给的是业务连接（multiStatements=false）。
	db, err := sql.Open("mysql", database.MigrateDSN(cfg.Database))
	if err != nil {
		return fmt.Errorf("打开迁移连接失败（host=%s port=%d db=%s user=%s）: %w",
			cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, cfg.Database.User, err)
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("加载内嵌迁移文件失败: %w", err)
	}

	driver, err := mysqldrv.WithInstance(db, &mysqldrv.Config{})
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("初始化迁移驱动失败: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("初始化 migrate 失败: %w", err)
	}

	// 注意：mysql 驱动的 Close 会连我们传进去的 *sql.DB 一起关，
	// 所以这里不再 defer db.Close()，否则是重复关闭。
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil || dbErr != nil {
			logger.Warn("关闭迁移资源时出错", "source_err", srcErr, "db_err", dbErr)
		}
	}()

	return fn(m, logger)
}
