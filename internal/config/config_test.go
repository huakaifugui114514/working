package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTemp 把内容写成临时文件并返回路径。
// 用 t.TempDir() 而不是 os.TempDir()：测试结束自动清理，且并发测试之间不会互相覆盖。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置文件失败: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") 返回错误: %v", err)
	}

	if cfg.App.Name != "knowflow-apid" {
		t.Errorf("app.name = %q, want %q", cfg.App.Name, "knowflow-apid")
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("server.addr = %q, want %q", cfg.Server.Addr, ":8080")
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("server.shutdown_timeout = %s, want 15s", cfg.Server.ShutdownTimeout)
	}
}

// TestLoadFileOverridesDefaults 验证第二层优先级：文件覆盖默认值，未出现的字段保留默认值。
func TestLoadFileOverridesDefaults(t *testing.T) {
	path := writeTemp(t, `
app:
  env: test
server:
  addr: "127.0.0.1:9000"
  read_timeout: 1s
log:
  format: text
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	// 文件里写了的：被覆盖
	if cfg.App.Env != "test" {
		t.Errorf("app.env = %q, want %q", cfg.App.Env, "test")
	}
	if cfg.Server.Addr != "127.0.0.1:9000" {
		t.Errorf("server.addr = %q, want %q", cfg.Server.Addr, "127.0.0.1:9000")
	}
	if cfg.Server.ReadTimeout != time.Second {
		t.Errorf("server.read_timeout = %s, want 1s（YAML 里的 1s 应能解析成 Duration）", cfg.Server.ReadTimeout)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("log.format = %q, want %q", cfg.Log.Format, "text")
	}

	// 文件里没写的：保留默认值
	if cfg.App.Name != "knowflow-apid" {
		t.Errorf("app.name = %q, 文件未指定时应保留默认值", cfg.App.Name)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("server.shutdown_timeout = %s, 文件未指定时应保留默认值 15s", cfg.Server.ShutdownTimeout)
	}
}

// TestEnvOverridesFile 验证最高优先级：环境变量压过文件。
// 注意：用了 t.Setenv 的测试不能 t.Parallel()——它改的是进程级环境变量，
// 并行执行会与其他测试互相干扰（这也是"测试要隔离"的一个具体例子）。
func TestEnvOverridesFile(t *testing.T) {
	path := writeTemp(t, `
app:
  env: dev
server:
  addr: "127.0.0.1:9000"
`)

	t.Setenv(EnvPrefix+"APP_ENV", "prod")
	t.Setenv(EnvPrefix+"SERVER_ADDR", ":18080")
	t.Setenv(EnvPrefix+"SERVER_SHUTDOWN_TIMEOUT", "3s")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "debug")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	if cfg.App.Env != "prod" {
		t.Errorf("app.env = %q, want prod（环境变量应覆盖文件）", cfg.App.Env)
	}
	if cfg.Server.Addr != ":18080" {
		t.Errorf("server.addr = %q, want :18080（环境变量应覆盖文件）", cfg.Server.Addr)
	}
	if cfg.Server.ShutdownTimeout != 3*time.Second {
		t.Errorf("server.shutdown_timeout = %s, want 3s", cfg.Server.ShutdownTimeout)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string // 空字符串表示不写文件
		env         map[string]string
		wantContain string // 错误信息里必须出现的关键字，避免"报错了但不知道错在哪"
	}{
		{
			name:        "未知字段必须报错（防配置静默失效）",
			yaml:        "server:\n  shutdow_timeout: 15s\n",
			wantContain: "shutdow_timeout",
		},
		{
			name:        "非法环境名",
			yaml:        "app:\n  env: staging\n",
			wantContain: "app.env",
		},
		{
			name:        "非法日志级别",
			yaml:        "log:\n  level: verbose\n",
			wantContain: "log.level",
		},
		{
			name:        "超时不能为负",
			yaml:        "server:\n  read_timeout: -1s\n",
			wantContain: "read_timeout",
		},
		{
			name:        "环境变量里的非法时长必须报错",
			env:         map[string]string{EnvPrefix + "SERVER_SHUTDOWN_TIMEOUT": "十五秒"},
			wantContain: "SERVER_SHUTDOWN_TIMEOUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.yaml != "" {
				path = writeTemp(t, tt.yaml)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("期望返回错误，实际为 nil")
			}
			if !strings.Contains(err.Error(), tt.wantContain) {
				t.Errorf("错误信息 = %q, 期望包含 %q", err.Error(), tt.wantContain)
			}
		})
	}
}

// TestLoadMissingFile 文件不存在时必须报错并带上路径，
// 而不是"悄悄用默认值跑起来"——那种行为会让人误以为配置生效了。
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-exist.yaml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("配置文件不存在时应当报错")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("错误信息里应包含出问题的路径 %q，实际：%v", path, err)
	}
}
