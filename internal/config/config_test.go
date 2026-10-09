package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeTemp 把 YAML 内容写成临时文件并返回路径。
// 用 t.TempDir()：测试结束自动清理，并发测试之间也不会互相覆盖。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置文件失败: %v", err)
	}
	return path
}

// clearEnv 清掉本包认识的所有 KF_* 变量，并在测试结束后恢复。
// 为什么需要：Load("") 只在"默认值 + 环境变量"这一层工作，
// 如果开发者的 shell 里恰好设了 KF_LOG_LEVEL，测试结果就取决于机器——那不是测试，是抽奖。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{envAppEnv, envServerAddr, envServerShutdown, envLogLevel, envLogFormat} {
		old, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("清除环境变量 %s 失败: %v", key, err)
		}
		if existed {
			t.Cleanup(func() { _ = os.Setenv(key, old) })
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") 返回错误: %v", err)
	}

	want := Default()
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load(\"\") 与 Default() 不一致：\n得到: %+v\n期望: %+v", cfg, want)
	}
}

// TestConfigFileMatchesDefaults 是"两处真源"的一致性契约：
// Default() 是唯一真源，configs/config.yaml 是提交给所有人的默认配置，两者必须逐字段相同。
// 改默认值忘了改另一边，这个测试就会红。
//
// 刻意调 applyFile 而不是 Load：Load 会把环境变量也合进来，测试结果就取决于跑测试的机器。
func TestConfigFileMatchesDefaults(t *testing.T) {
	got := Default()
	if err := applyFile(got, filepath.Join("..", "..", "configs", "config.yaml")); err != nil {
		t.Fatalf("解析 configs/config.yaml 失败: %v", err)
	}

	want := Default()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("configs/config.yaml 与 Default() 不一致：\n文件解析结果: %+v\n代码默认值:   %+v", got, want)
	}
}

// TestFileOverridesDefaults 验证第二层：文件覆盖默认值，且文件里没写的字段保留默认值。
func TestFileOverridesDefaults(t *testing.T) {
	clearEnv(t)

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
	if cfg.App.Env != EnvTest {
		t.Errorf("app.env = %q, want test", cfg.App.Env)
	}
	if cfg.Server.Addr != "127.0.0.1:9000" {
		t.Errorf("server.addr = %q, want 127.0.0.1:9000", cfg.Server.Addr)
	}
	if cfg.Server.ReadTimeout != time.Second {
		t.Errorf("server.read_timeout = %s, want 1s（YAML 的 1s 应能解析成 Duration）", cfg.Server.ReadTimeout)
	}
	if cfg.Log.Format != LogFormatText {
		t.Errorf("log.format = %q, want text", cfg.Log.Format)
	}

	// 文件里没写的：保留默认值
	if cfg.App.Name != "knowflow-apid" {
		t.Errorf("app.name = %q, 文件未指定时应保留默认值", cfg.App.Name)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("server.shutdown_timeout = %s, 文件未指定时应保留默认值 15s", cfg.Server.ShutdownTimeout)
	}
}

// TestEnvOverridesFile 验证最高优先级，也是本包最容易写错的地方：
// 传了 path 也照样被环境变量覆盖（两层是顺序执行，不是二选一）。
//
// 注意：用了 t.Setenv 的测试不能 t.Parallel()——它改的是进程级环境变量。
func TestEnvOverridesFile(t *testing.T) {
	path := writeTemp(t, `
app:
  env: dev
server:
  addr: "127.0.0.1:9000"
  shutdown_timeout: 9s
`)

	t.Setenv(envAppEnv, "prod")
	t.Setenv(envServerAddr, ":18080")
	t.Setenv(envServerShutdown, "3s")
	t.Setenv(envLogLevel, "debug")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	if cfg.App.Env != EnvProd {
		t.Errorf("app.env = %q, want prod（环境变量应覆盖文件）", cfg.App.Env)
	}
	if cfg.Server.Addr != ":18080" {
		t.Errorf("server.addr = %q, want :18080（环境变量应覆盖文件）", cfg.Server.Addr)
	}
	if cfg.Server.ShutdownTimeout != 3*time.Second {
		t.Errorf("server.shutdown_timeout = %s, want 3s", cfg.Server.ShutdownTimeout)
	}
	if cfg.Log.Level != LogLevelDebug {
		t.Errorf("log.level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string // 空字符串表示不写文件（path 传 ""）
		env         map[string]string
		wantContain string // 错误信息里必须出现的关键字：避免"报错了但不知道错在哪"
	}{
		{
			name:        "未知字段必须报错（防配置静默失效）",
			yaml:        "server:\n  shutdow_timeout: 15s\n",
			wantContain: "shutdow_timeout",
		},
		{
			name:        "多份 YAML 文档必须报错（防第二份被静默忽略）",
			yaml:        "server:\n  addr: \":8080\"\n---\nserver:\n  addr: \":9999\"\n",
			wantContain: "多份 YAML 文档",
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
			wantContain: "server.read_timeout",
		},
		{
			name:        "环境变量里的非法时长",
			env:         map[string]string{envServerShutdown: "十五秒"},
			wantContain: envServerShutdown,
		},
		{
			name:        "环境变量里的非法日志级别",
			env:         map[string]string{envLogLevel: "verbose"},
			wantContain: "log.level",
		},
		{
			name:        "环境变量显式设成空串也要被拦住",
			env:         map[string]string{envServerAddr: ""},
			wantContain: "server.addr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)

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
				t.Errorf("错误信息 = %q，期望包含 %q", err.Error(), tt.wantContain)
			}
		})
	}
}

// TestLoadMissingFile：文件不存在要报错并带上路径与补救方式，
// 而不是"悄悄用默认值跑起来"——那会让人以为配置生效了。
func TestLoadMissingFile(t *testing.T) {
	clearEnv(t)

	path := filepath.Join(t.TempDir(), "not-exist.yaml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("配置文件不存在时应当报错")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("错误信息里应包含出问题的路径 %q，实际: %v", path, err)
	}
	if !strings.Contains(err.Error(), EnvConfigFile) {
		t.Errorf("错误信息里应告诉用户可以用 %s 指定配置文件，实际: %v", EnvConfigFile, err)
	}
}

// TestValidateCollectsAllProblems 钉住"一次报出全部问题"这个契约：
// 只断言 err != nil 的话，把 Validate 改成"遇到第一个错就返回"测试照样绿。
func TestValidateCollectsAllProblems(t *testing.T) {
	cfg := &Config{} // 全零值：每一项都应该不合格

	err := cfg.Validate()
	if err == nil {
		t.Fatal("空配置必须校验失败")
	}

	want := []string{
		"app.name",
		"app.env",
		"server.addr",
		"server.read_timeout",
		"server.write_timeout",
		"server.idle_timeout",
		"server.shutdown_timeout",
		"log.level",
		"log.format",
	}
	for _, field := range want {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("校验错误里缺少 %q，实际输出:\n%v", field, err)
		}
	}
}

// TestValidConfigPasses：合法配置不能被误杀（防止 Validate 写成"永远报错"也能过前面的测试）。
func TestValidConfigPasses(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("Default() 应当通过校验，实际: %v", err)
	}
}
