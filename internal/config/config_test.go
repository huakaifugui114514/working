package config

import (
	"bytes"
	"fmt"
	"log/slog"
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
	for _, key := range []string{
		envAppEnv, envServerAddr, envServerShutdown, envLogLevel, envLogFormat,
		envDatabaseHost, envDatabasePort, envDatabaseName, envDatabaseUser, envDatabasePassword,
		envDatabaseMaxOpenConns, envDatabaseMaxIdleConns, envDatabaseConnLife, envDatabaseConnIdle,
		envLLMBaseURL, envLLMModel, envLLMAPIKey, envLLMTimeout, envLLMMaxTokens,
		envEmbeddingOllamaURL, envEmbeddingModel, envEmbeddingTimeout, envEmbeddingBatchSize,
		envQdrantURL, envQdrantCollection, envQdrantVectorSize, envQdrantTimeout,
		envRedisAddr, envRedisPassword, envRedisDB, envRedisTimeout, envRedisSearchTTL,
		envRetrievalMode, envRetrievalCandidates, envRetrievalRRFK, envAuthAPIKey,
	} {
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
	t.Setenv(envAuthAPIKey, "test-api-key")
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
		"database.host",
		"database.port",
		"database.name",
		"database.user",
		"database.max_open_conns",
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

// ---- Database 相关 ----

// TestDefaultDatabaseValues 单独钉住默认值本身。
// 为什么不能只靠 TestConfigFileMatchesDefaults：那条只保证"YAML 与 Default() 一致"。
// 如果有人把 port 从 3306 改成 3307 并同步改了 YAML，它照样绿——默认值已经错了却没人发现。
func TestDefaultDatabaseValues(t *testing.T) {
	db := Default().Database

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"host", db.Host, "127.0.0.1"},
		{"port", db.Port, 3306},
		{"name", db.Name, "knowflow"},
		{"user", db.User, "knowflow"},
		{"password", db.Password, ""}, // 默认必须为空：密钥只能从环境变量来
		{"max_open_conns", db.MaxOpenConns, 10},
		{"max_idle_conns", db.MaxIdleConns, 5},
		{"conn_max_lifetime", db.ConnMaxLifetime, 30 * time.Minute},
		{"conn_max_idle_time", db.ConnMaxIdleTime, 5 * time.Minute},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("database.%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

// TestDatabaseEnvOverrides 验证 9 个 KF_DATABASE_* 变量都能覆盖默认值。
func TestDatabaseEnvOverrides(t *testing.T) {
	clearEnv(t)

	t.Setenv(envDatabaseHost, "db.internal")
	t.Setenv(envDatabasePort, "13306")
	t.Setenv(envDatabaseName, "kb")
	t.Setenv(envDatabaseUser, "svc")
	t.Setenv(envDatabasePassword, "from-env")
	t.Setenv(envDatabaseMaxOpenConns, "50")
	t.Setenv(envDatabaseMaxIdleConns, "20")
	t.Setenv(envDatabaseConnLife, "45m")
	t.Setenv(envDatabaseConnIdle, "3m")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	db := cfg.Database
	if db.Host != "db.internal" || db.Port != 13306 || db.Name != "kb" || db.User != "svc" {
		t.Errorf("字符串/整数覆盖失败: %+v", db)
	}
	if db.Password != "from-env" {
		t.Error("KF_DATABASE_PASSWORD 应覆盖 password（这是密码的唯一来源）")
	}
	if db.MaxOpenConns != 50 || db.MaxIdleConns != 20 {
		t.Errorf("连接池覆盖失败: open=%d idle=%d", db.MaxOpenConns, db.MaxIdleConns)
	}
	if db.ConnMaxLifetime != 45*time.Minute || db.ConnMaxIdleTime != 3*time.Minute {
		t.Errorf("时长覆盖失败: lifetime=%s idle=%s", db.ConnMaxLifetime, db.ConnMaxIdleTime)
	}
}

// TestDatabaseEnvParseErrors 钉住"解析失败要说清楚是哪个变量、什么值"。
func TestDatabaseEnvParseErrors(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{envDatabasePort, "abc"},
		{envDatabaseMaxOpenConns, "十"},
		{envDatabaseMaxIdleConns, "3.5"},
		{envDatabaseConnLife, "30"},  // 缺单位
		{envDatabaseConnIdle, "5分钟"}, // 单位不合法
	}

	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load("")
			if err == nil {
				t.Fatalf("%s=%q 应当报错", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("错误信息里应包含变量名 %s，实际: %v", tt.key, err)
			}
			if !strings.Contains(err.Error(), tt.value) {
				t.Errorf("错误信息里应包含非法值 %q，实际: %v", tt.value, err)
			}
		})
	}
}

// TestValidateDatabase 覆盖 Database 的每条规则与边界。
func TestValidateDatabase(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // 空表示应当通过
	}{
		{name: "默认值合法", mutate: func(*Config) {}},
		{name: "port 下边界 1", mutate: func(c *Config) { c.Database.Port = 1 }},
		{name: "port 上边界 65535", mutate: func(c *Config) { c.Database.Port = 65535 }},
		{name: "max_idle 等于 max_open", mutate: func(c *Config) {
			c.Database.MaxOpenConns = 10
			c.Database.MaxIdleConns = 10
		}},
		{name: "conn_max_lifetime 为 0（database/sql 语义：不限制）", mutate: func(c *Config) {
			c.Database.ConnMaxLifetime = 0
		}},
		{name: "password 为空不报错（刻意不校验）", mutate: func(c *Config) { c.Database.Password = "" }},

		{name: "host 为空", mutate: func(c *Config) { c.Database.Host = "" }, wantErr: "database.host"},
		{name: "port 为 0", mutate: func(c *Config) { c.Database.Port = 0 }, wantErr: "database.port"},
		{name: "port 超过 65535", mutate: func(c *Config) { c.Database.Port = 65536 }, wantErr: "database.port"},
		{name: "name 为空", mutate: func(c *Config) { c.Database.Name = "" }, wantErr: "database.name"},
		{name: "user 为空", mutate: func(c *Config) { c.Database.User = "" }, wantErr: "database.user"},
		{name: "max_open_conns 为 0", mutate: func(c *Config) { c.Database.MaxOpenConns = 0 }, wantErr: "database.max_open_conns"},
		{name: "max_idle 为负", mutate: func(c *Config) { c.Database.MaxIdleConns = -1 }, wantErr: "database.max_idle_conns"},
		{name: "max_idle 大于 max_open", mutate: func(c *Config) {
			c.Database.MaxOpenConns = 5
			c.Database.MaxIdleConns = 10
		}, wantErr: "database.max_idle_conns"},
		{name: "conn_max_lifetime 为负", mutate: func(c *Config) {
			c.Database.ConnMaxLifetime = -time.Second
		}, wantErr: "database.conn_max_lifetime"},
		{name: "conn_max_idle_time 为负", mutate: func(c *Config) {
			c.Database.ConnMaxIdleTime = -time.Second
		}, wantErr: "database.conn_max_idle_time"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过校验，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错（应包含 %q），实际为 nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("错误信息应包含 %q，实际: %v", tt.wantErr, err)
			}
		})
	}
}

// ---- Auth 相关 ----

func TestAuthEnvOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envAuthAPIKey, "secret")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.APIKey != "secret" {
		t.Fatalf("api_key = %q", cfg.Auth.APIKey)
	}
}

func TestValidateProdRequiresAPIKey(t *testing.T) {
	cfg := Default()
	cfg.App.Env = EnvProd
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "auth.api_key") {
		t.Fatalf("prod 无 key 应报错，实际: %v", err)
	}
	cfg.Auth.APIKey = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("prod 有 key 应通过: %v", err)
	}
}

// ---- Retrieval 相关 ----

func TestDefaultRetrievalValues(t *testing.T) {
	r := Default().Retrieval
	if r.Mode != RetrievalFTS || r.Candidates != 20 || r.RRFK != 60 {
		t.Fatalf("retrieval = %+v", r)
	}
}

func TestRetrievalEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv(envRetrievalMode, "hybrid")
	t.Setenv(envRetrievalCandidates, "30")
	t.Setenv(envRetrievalRRFK, "80")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.Mode != RetrievalHybrid || cfg.Retrieval.Candidates != 30 || cfg.Retrieval.RRFK != 80 {
		t.Fatalf("retrieval = %+v", cfg.Retrieval)
	}
}

func TestValidateRetrieval(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "默认值合法", mutate: func(*Config) {}},
		{name: "fts", mutate: func(c *Config) { c.Retrieval.Mode = RetrievalFTS }},
		{name: "vector", mutate: func(c *Config) { c.Retrieval.Mode = RetrievalVector }},
		{name: "hybrid", mutate: func(c *Config) { c.Retrieval.Mode = RetrievalHybrid }},
		{name: "mode 非法", mutate: func(c *Config) { c.Retrieval.Mode = "banana" }, wantErr: "retrieval.mode"},
		{name: "candidates 为 0", mutate: func(c *Config) { c.Retrieval.Candidates = 0 }, wantErr: "retrieval.candidates"},
		{name: "rrf_k 为 0", mutate: func(c *Config) { c.Retrieval.RRFK = 0 }, wantErr: "retrieval.rrf_k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tt.wantErr, err)
			}
		})
	}
}

// ---- Embedding / Qdrant 相关 ----

func TestDefaultEmbeddingQdrantValues(t *testing.T) {
	emb := Default().Embedding
	qd := Default().Qdrant
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"embedding.ollama_url", emb.OllamaURL, "http://127.0.0.1:11434"},
		{"embedding.model", emb.Model, "bge-m3"},
		{"embedding.timeout", emb.Timeout, 120 * time.Second},
		{"embedding.batch_size", emb.BatchSize, 16},
		{"qdrant.url", qd.URL, "http://127.0.0.1:6333"},
		{"qdrant.collection", qd.Collection, "document_chunks"},
		{"qdrant.vector_size", qd.VectorSize, 1024},
		{"qdrant.timeout", qd.Timeout, 10 * time.Second},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestEmbeddingQdrantEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv(envEmbeddingOllamaURL, "http://ollama.internal:11434")
	t.Setenv(envEmbeddingModel, "custom-embed")
	t.Setenv(envEmbeddingTimeout, "30s")
	t.Setenv(envEmbeddingBatchSize, "32")
	t.Setenv(envQdrantURL, "http://qdrant.internal:6333")
	t.Setenv(envQdrantCollection, "custom_chunks")
	t.Setenv(envQdrantVectorSize, "768")
	t.Setenv(envQdrantTimeout, "5s")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedding.OllamaURL != "http://ollama.internal:11434" ||
		cfg.Embedding.Model != "custom-embed" ||
		cfg.Embedding.Timeout != 30*time.Second ||
		cfg.Embedding.BatchSize != 32 {
		t.Fatalf("embedding = %+v", cfg.Embedding)
	}
	if cfg.Qdrant.URL != "http://qdrant.internal:6333" ||
		cfg.Qdrant.Collection != "custom_chunks" ||
		cfg.Qdrant.VectorSize != 768 ||
		cfg.Qdrant.Timeout != 5*time.Second {
		t.Fatalf("qdrant = %+v", cfg.Qdrant)
	}
}

func TestValidateEmbeddingQdrant(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "默认值合法", mutate: func(*Config) {}},
		{name: "ollama_url 为空", mutate: func(c *Config) { c.Embedding.OllamaURL = "" }, wantErr: "embedding.ollama_url"},
		{name: "ollama_url 非法", mutate: func(c *Config) { c.Embedding.OllamaURL = "file:///tmp" }, wantErr: "embedding.ollama_url"},
		{name: "model 为空", mutate: func(c *Config) { c.Embedding.Model = "" }, wantErr: "embedding.model"},
		{name: "timeout 为 0", mutate: func(c *Config) { c.Embedding.Timeout = 0 }, wantErr: "embedding.timeout"},
		{name: "batch_size 为 0", mutate: func(c *Config) { c.Embedding.BatchSize = 0 }, wantErr: "embedding.batch_size"},
		{name: "qdrant url 为空", mutate: func(c *Config) { c.Qdrant.URL = "" }, wantErr: "qdrant.url"},
		{name: "collection 为空", mutate: func(c *Config) { c.Qdrant.Collection = "" }, wantErr: "qdrant.collection"},
		{name: "vector_size 为 0", mutate: func(c *Config) { c.Qdrant.VectorSize = 0 }, wantErr: "qdrant.vector_size"},
		{name: "qdrant timeout 为 0", mutate: func(c *Config) { c.Qdrant.Timeout = 0 }, wantErr: "qdrant.timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tt.wantErr, err)
			}
		})
	}
}

// ---- LLM 相关 ----

func TestDefaultLLMValues(t *testing.T) {
	llm := Default().LLM

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"base_url", llm.BaseURL, "https://api.deepseek.com"},
		{"model", llm.Model, "deepseek-chat"},
		{"api_key", llm.APIKey, ""}, // 默认必须为空：密钥只能从环境变量来
		{"timeout", llm.Timeout, 60 * time.Second},
		{"max_tokens", llm.MaxTokens, 1024},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("llm.%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestLLMEnvOverrides(t *testing.T) {
	clearEnv(t)

	t.Setenv(envLLMBaseURL, "https://llm.example.com/v1")
	t.Setenv(envLLMModel, "test-model")
	t.Setenv(envLLMAPIKey, "from-env")
	t.Setenv(envLLMTimeout, "15s")
	t.Setenv(envLLMMaxTokens, "256")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	llm := cfg.LLM
	if llm.BaseURL != "https://llm.example.com/v1" || llm.Model != "test-model" {
		t.Errorf("字符串覆盖失败: %+v", llm)
	}
	if llm.APIKey != "from-env" {
		t.Error("KF_LLM_API_KEY 应覆盖 api_key（这是密钥的唯一来源）")
	}
	if llm.Timeout != 15*time.Second || llm.MaxTokens != 256 {
		t.Errorf("timeout/max_tokens 覆盖失败: %+v", llm)
	}
}

func TestLLMEnvParseErrors(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{envLLMTimeout, "60"},
		{envLLMMaxTokens, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load("")
			if err == nil {
				t.Fatalf("%s=%q 应当报错", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("错误信息里应包含变量名 %s，实际: %v", tt.key, err)
			}
		})
	}
}

func TestValidateLLM(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "默认值合法", mutate: func(*Config) {}},
		{name: "api_key 为空合法", mutate: func(c *Config) { c.LLM.APIKey = "" }},
		{name: "base_url 为空", mutate: func(c *Config) { c.LLM.BaseURL = "" }, wantErr: "llm.base_url"},
		{name: "base_url 不是 http(s)", mutate: func(c *Config) { c.LLM.BaseURL = "file:///tmp" }, wantErr: "llm.base_url"},
		{name: "model 为空", mutate: func(c *Config) { c.LLM.Model = "" }, wantErr: "llm.model"},
		{name: "timeout 为 0", mutate: func(c *Config) { c.LLM.Timeout = 0 }, wantErr: "llm.timeout"},
		{name: "max_tokens 为 0", mutate: func(c *Config) { c.LLM.MaxTokens = 0 }, wantErr: "llm.max_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过校验，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错（应包含 %q），实际为 nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("错误信息应包含 %q，实际: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLLMRedaction(t *testing.T) {
	const secret = "LLMSecret123!"
	cfg := Default()
	cfg.LLM.APIKey = secret

	if out := fmt.Sprintf("%v", cfg.LLM); strings.Contains(out, secret) || !strings.Contains(out, "***") {
		t.Fatalf("fmt %%v 应脱敏: %s", out)
	}
	if out := fmt.Sprintf("%+v", *cfg); strings.Contains(out, secret) || !strings.Contains(out, "***") {
		t.Fatalf("fmt %%+v 应脱敏: %s", out)
	}

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("配置已加载", "llm", cfg.LLM)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("slog 输出泄露 API key: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "llm.api_key=***") {
		t.Fatalf("slog 输出应包含 llm.api_key=***: %s", buf.String())
	}
}

// TestDatabasePasswordRejectedInYAML：密码只允许来自环境变量。
// 配置文件里写 password 必须被 KnownFields 拒绝——否则它会静默失效或意外生效。
func TestDatabasePasswordRejectedInYAML(t *testing.T) {
	clearEnv(t)

	path := writeTemp(t, "database:\n  password: written-in-file\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("YAML 里出现 password 应当报错")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("错误信息应提到 password，实际: %v", err)
	}
}

// TestDatabaseRedaction 钉住三条泄露路径：fmt %v、fmt %+v、slog。
func TestDatabaseRedaction(t *testing.T) {
	const secret = "SuperSecret123!" // 固定假值：失败信息里也不会出现真密码

	cfg := Default()
	cfg.Database.Password = secret

	t.Run("fmt %v", func(t *testing.T) {
		assertRedacted(t, fmt.Sprintf("%v", cfg.Database), secret)
	})

	t.Run("fmt %+v 整个 Config", func(t *testing.T) {
		assertRedacted(t, fmt.Sprintf("%+v", *cfg), secret)
	})

	t.Run("slog 结构化日志", func(t *testing.T) {
		var buf bytes.Buffer
		slog.New(slog.NewTextHandler(&buf, nil)).Info("配置已加载", "database", cfg.Database)

		out := buf.String()
		assertRedacted(t, out, secret)
		// LogValue 的价值：日志平台能按字段检索，而不是拿到一坨格式化字符串
		if !strings.Contains(out, "database.password=***") {
			t.Errorf("slog 输出应是结构化分组且 password=***，实际: %s", out)
		}
	})
}

// assertRedacted 断言输出既不含明文密码，也不含 fmt 的格式错误标记（如 %!s(int=3306)）。
func assertRedacted(t *testing.T, out, secret string) {
	t.Helper()
	if strings.Contains(out, secret) {
		t.Errorf("输出泄露了明文密码: %s", out)
	}
	if strings.Contains(out, "%!") {
		t.Errorf("输出里有 fmt 格式错误标记（多半是 %%s 配了非字符串）: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("输出里应有脱敏标记 ***，实际: %s", out)
	}
}

func TestDefaultRedisValues(t *testing.T) {
	clearEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Redis.Addr != "" {
		t.Fatalf("redis.addr = %q, want empty（默认关闭缓存）", cfg.Redis.Addr)
	}
	if cfg.Redis.Timeout != 2*time.Second || cfg.Redis.SearchTTL != 30*time.Second {
		t.Fatalf("redis timeouts = %s/%s", cfg.Redis.Timeout, cfg.Redis.SearchTTL)
	}
}

func TestRedisEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv(envRedisAddr, "redis:6379")
	t.Setenv(envRedisPassword, "secret")
	t.Setenv(envRedisDB, "3")
	t.Setenv(envRedisTimeout, "1500ms")
	t.Setenv(envRedisSearchTTL, "45s")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Redis.Addr != "redis:6379" || cfg.Redis.Password != "secret" || cfg.Redis.DB != 3 {
		t.Fatalf("redis = %+v", cfg.Redis)
	}
	if cfg.Redis.Timeout != 1500*time.Millisecond || cfg.Redis.SearchTTL != 45*time.Second {
		t.Fatalf("redis timeouts = %s/%s", cfg.Redis.Timeout, cfg.Redis.SearchTTL)
	}
}

func TestRedisEnvParseErrors(t *testing.T) {
	tests := []struct{ key, value string }{
		{envRedisDB, "x"},
		{envRedisTimeout, "nope"},
		{envRedisSearchTTL, "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := Load(""); err == nil {
				t.Fatal("应当返回解析错误")
			}
		})
	}
}

func TestRedisValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "db negative", mutate: func(c *Config) { c.Redis.DB = -1 }},
		{name: "db too large", mutate: func(c *Config) { c.Redis.DB = 16 }},
		{name: "timeout zero", mutate: func(c *Config) { c.Redis.Timeout = 0 }},
		{name: "ttl zero", mutate: func(c *Config) { c.Redis.SearchTTL = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("应当校验失败")
			}
		})
	}
}
