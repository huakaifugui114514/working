package database

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/huakaifugui114514/working/internal/config"
)

// discardLogger 让测试不往 stdout 刷日志。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// parseDSN 用驱动自己的解析器反解，断言"驱动理解成了什么"，
// 而不是断言我们拼出来的字符串长什么样——后者会把实现细节钉死。
func parseDSN(t *testing.T, dsn string) *mysql.Config {
	t.Helper()
	p, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("驱动无法解析我们生成的 DSN: %v", err)
	}
	return p
}

func TestDSN(t *testing.T) {
	cfg := config.Default().Database
	// 故意用含 @ : / ? # 的密码：手拼 DSN 在这个输入下必然拼坏。
	// 这是本测试最重要的价值——它证明"没有手拼"。
	cfg.Password = "p@ss:/?w#rd"

	p := parseDSN(t, DSN(cfg))

	if p.Passwd != cfg.Password {
		t.Errorf("密码往返失败：得到 %q，期望 %q（手拼 DSN 会在这里暴露）", p.Passwd, cfg.Password)
	}
	if p.User != cfg.User {
		t.Errorf("user = %q, want %q", p.User, cfg.User)
	}
	if p.DBName != cfg.Name {
		t.Errorf("dbname = %q, want %q", p.DBName, cfg.Name)
	}
	if p.Addr != "127.0.0.1:3306" {
		t.Errorf("addr = %q, want 127.0.0.1:3306", p.Addr)
	}
	if !p.ParseTime {
		t.Error("parseTime 必须为 true，否则 DATETIME 扫不进 time.Time")
	}
	if p.Loc != time.UTC {
		t.Errorf("loc = %v, want UTC（约定：时间一律存 UTC）", p.Loc)
	}
	if p.MultiStatements {
		t.Error("业务连接的 multiStatements 必须为 false：开了会把 SQL 注入的危害放大成任意多条语句")
	}
	if p.Timeout != 3*time.Second {
		t.Errorf("timeout = %v, want 3s（拨号上限）", p.Timeout)
	}
}

func TestMigrateDSN(t *testing.T) {
	cfg := config.Default().Database
	cfg.Password = "p@ss:/?w#rd"

	migrate := parseDSN(t, MigrateDSN(cfg))
	if !migrate.MultiStatements {
		t.Error("迁移连接的 multiStatements 必须为 true：一个迁移文件里常有多条 DDL")
	}
	if migrate.Passwd != cfg.Password {
		t.Errorf("密码往返失败：得到 %q", migrate.Passwd)
	}

	// 除 multiStatements 外，两条串必须完全一致——否则"只差一个开关"的约定就破了
	business := parseDSN(t, DSN(cfg))
	if migrate.User != business.User || migrate.Addr != business.Addr ||
		migrate.DBName != business.DBName || migrate.ParseTime != business.ParseTime ||
		migrate.Loc != business.Loc || migrate.Timeout != business.Timeout {
		t.Error("迁移串与业务串除 multiStatements 外应当完全一致")
	}
}

func TestDSNIsIPv6Safe(t *testing.T) {
	cfg := config.Default().Database
	cfg.Host = "::1"

	p := parseDSN(t, DSN(cfg))
	// net.JoinHostPort 会给 IPv6 加方括号；手拼 "host:port" 会得到 "::1:3306"，解析直接错
	if p.Addr != "[::1]:3306" {
		t.Errorf("addr = %q, want [::1]:3306（IPv6 必须带方括号）", p.Addr)
	}
}

// TestOpenErrorDoesNotLeakPassword 钉住红线：DSN 含明文密码，绝不能进错误消息。
func TestOpenErrorDoesNotLeakPassword(t *testing.T) {
	cfg := config.Default().Database
	cfg.Host, cfg.Port = "127.0.0.1", 1 // 必失败：没有服务会监听 1 端口
	cfg.Password = "SuperSecret123!"

	_, err := Open(context.Background(), cfg, discardLogger())
	if err == nil {
		t.Fatal("连不上时应当返回错误")
	}

	out := err.Error()
	if strings.Contains(out, cfg.Password) {
		t.Errorf("错误消息泄露了明文密码: %s", out)
	}
	// 同时要有定位信息，否则运维不知道连的是哪儿
	for _, want := range []string{"127.0.0.1", cfg.Name, cfg.User} {
		if !strings.Contains(out, want) {
			t.Errorf("错误消息应包含 %q 便于定位，实际: %s", want, out)
		}
	}
}
