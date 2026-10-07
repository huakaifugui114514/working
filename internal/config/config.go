// Package config 负责加载、覆盖与校验服务配置。
//
// 为什么不用 viper 这类配置框架：它们的隐式行为太多（自动读环境变量、自动监听文件、
// 键名大小写规则），出问题时很难回答"这个值到底是从哪来的"。本包的规则只有一条，
// 而且写在代码里看得见：
//
//	内置默认值 < YAML 文件 < 环境变量（KF_ 前缀）
//
// 为什么密钥只能从环境变量读：配置文件会跟着 Git 走遍所有 clone，环境变量不会。
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// 环境变量与路径约定。
const (
	// EnvPrefix 是所有覆盖项的前缀，避免与其他程序的变量重名。
	EnvPrefix = "KF_"
	// EnvConfigFile 指定配置文件路径。它属于"启动参数"，不是 Config 的字段，
	// 所以由 main 读取后传给 Load，而不是塞进配置结构里。
	EnvConfigFile = EnvPrefix + "CONFIG_FILE"
	// DefaultPath 是未显式指定配置文件时使用的路径（相对进程工作目录）。
	DefaultPath = "configs/config.yaml"
)

// 允许的取值集合。用 map 而不是一长串 if：将来加一个取值只改一处。
var (
	validEnvs       = map[string]bool{"dev": true, "test": true, "prod": true}
	validLogLevels  = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	validLogFormats = map[string]bool{"json": true, "text": true}
)

// Config 是服务的全部配置。
type Config struct {
	App    App    `yaml:"app"`
	Server Server `yaml:"server"`
	Log    Log    `yaml:"log"`
}

// App 是应用级标识。
type App struct {
	Name string `yaml:"name"`
	Env  string `yaml:"env"`
}

// Server 是 HTTP 服务端参数。
// 这几个超时不是"可选项"：少了它们，一个慢客户端就能一直占着连接，把服务拖垮。
type Server struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Log 是日志参数。
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default 返回内置默认值，即优先级最低的那一层。
func Default() *Config {
	return &Config{
		App: App{Name: "knowflow-apid", Env: "dev"},
		Server: Server{
			Addr:            ":8080",
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Log: Log{Level: "info", Format: "json"},
	}
}

// Load 按「默认值 < 文件 < 环境变量」组装配置，并在返回前校验一次。
// path 为空表示只用默认值 + 环境变量（例如容器里全部通过环境变量注入）。
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		if err := applyFile(cfg, path); err != nil {
			return nil, err
		}
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyFile 把 YAML 文件读进 cfg（只覆盖文件里出现的字段，其余保留默认值）。
//
// KnownFields(true) 是刻意开的：默认情况下 YAML 里写错字段名（例如 shutdow_timeout）
// 会被静默忽略，表现为"配置改了但没生效"，这类问题最难查。严格模式让它当场报错。
func applyFile(cfg *Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开配置文件 %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	return nil
}

// applyEnv 用环境变量覆盖文件里的值。
//
// 这里刻意不做反射自动映射：显式列出的代价是几行代码，收益是"每个配置项的来源"
// 一眼可见。自动映射在配置项变多之后，会变成没人敢动的黑魔法。
// 注意：解析失败必须返回错误，不能忽略——静默失败是最难排查的一类 bug。
func applyEnv(cfg *Config) error {
	if v := os.Getenv(EnvPrefix + "APP_ENV"); v != "" {
		cfg.App.Env = v
	}
	if v := os.Getenv(EnvPrefix + "SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv(EnvPrefix + "SERVER_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("环境变量 %sSERVER_SHUTDOWN_TIMEOUT=%q 不是合法时长（示例：15s）: %w", EnvPrefix, v, err)
		}
		cfg.Server.ShutdownTimeout = d
	}
	if v := os.Getenv(EnvPrefix + "LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv(EnvPrefix + "LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	return nil
}

// Validate 校验配置是否自洽。
//
// 启动阶段宁可失败也不要带着错误配置跑起来：一个写错的 addr 会等到第一个请求
// 才暴露，而那时你已经在排查业务问题了。用 errors.Join 一次报出全部问题。
func (c *Config) Validate() error {
	var errs []error

	if c.App.Name == "" {
		errs = append(errs, errors.New("app.name 不能为空"))
	}
	if !validEnvs[c.App.Env] {
		errs = append(errs, fmt.Errorf("app.env=%q 非法，只允许 dev/test/prod", c.App.Env))
	}
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr 不能为空"))
	}
	if c.Server.ReadTimeout <= 0 {
		errs = append(errs, fmt.Errorf("server.read_timeout 必须为正数，当前 %s", c.Server.ReadTimeout))
	}
	if c.Server.WriteTimeout <= 0 {
		errs = append(errs, fmt.Errorf("server.write_timeout 必须为正数，当前 %s", c.Server.WriteTimeout))
	}
	if c.Server.IdleTimeout <= 0 {
		errs = append(errs, fmt.Errorf("server.idle_timeout 必须为正数，当前 %s", c.Server.IdleTimeout))
	}
	if c.Server.ShutdownTimeout <= 0 {
		errs = append(errs, fmt.Errorf("server.shutdown_timeout 必须为正数，当前 %s", c.Server.ShutdownTimeout))
	}
	if !validLogLevels[c.Log.Level] {
		errs = append(errs, fmt.Errorf("log.level=%q 非法，只允许 debug/info/warn/error", c.Log.Level))
	}
	if !validLogFormats[c.Log.Format] {
		errs = append(errs, fmt.Errorf("log.format=%q 非法，只允许 json/text", c.Log.Format))
	}

	return errors.Join(errs...)
}
