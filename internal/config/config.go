// Package config 负责加载、覆盖与校验服务配置。
//
// 规则只有一条，而且是可见的：内置默认值 < YAML 文件 < 环境变量（KF_ 前缀）。
// 为什么不用 viper 这类配置框架：隐式行为太多（自动读环境变量、自动监听文件、键名大小写规则），
// 出问题时很难回答"这个值到底是从哪来的"。
//
// 为什么密钥只能从环境变量读：配置文件会跟着 Git 走遍所有 clone，环境变量不会。
//
// 取值集合（env / log.level / log.format）用「字符串常量 + 自定义类型」表达——
// Go 没有 enum 关键字，而且 const 本身没有类型安全（En("banana") 也能编译），
// 所以每个类型都提供 Valid()，校验统一在 Validate 里做：
// 环境变量这条路径不经过 YAML 解析，校验只能靠 Validate 兜底，一步都不能省。
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// 环境变量与路径约定。
const (
	// EnvPrefix 是所有覆盖项的前缀，避免与其他程序的变量重名。
	EnvPrefix = "KF_"
	// EnvConfigFile 指定配置文件路径。它属于"启动参数"，不是 Config 的字段，
	// 所以由 main 读取后传给 Load。
	EnvConfigFile = EnvPrefix + "CONFIG_FILE"
	// DefaultPath 是未显式指定配置文件时使用的路径（相对进程工作目录）。
	DefaultPath = "configs/config.yaml"

	// 会被环境变量覆盖的字段，一项一个常量。
	// 用常量而不是散落的字符串字面量：拼错会在编译期暴露，测试也能直接引用。
	// 注意：新增一项 = 新增一个对外承诺（部署脚本/ConfigMap 会依赖它），
	// 要同步更新 README 的配置表。
	envAppEnv         = EnvPrefix + "APP_ENV"
	envServerAddr     = EnvPrefix + "SERVER_ADDR"
	envServerShutdown = EnvPrefix + "SERVER_SHUTDOWN_TIMEOUT"
	envLogLevel       = EnvPrefix + "LOG_LEVEL"
	envLogFormat      = EnvPrefix + "LOG_FORMAT"

	// 数据库相关。注意 password 也在其中——它是**唯一**的来源：
	// Database.Password 的 yaml tag 是 "-"，配置文件里写它会被 KnownFields 直接拒绝。
	envDatabaseHost         = EnvPrefix + "DATABASE_HOST"
	envDatabasePort         = EnvPrefix + "DATABASE_PORT"
	envDatabaseName         = EnvPrefix + "DATABASE_NAME"
	envDatabaseUser         = EnvPrefix + "DATABASE_USER"
	envDatabasePassword     = EnvPrefix + "DATABASE_PASSWORD"
	envDatabaseMaxOpenConns = EnvPrefix + "DATABASE_MAX_OPEN_CONNS"
	envDatabaseMaxIdleConns = EnvPrefix + "DATABASE_MAX_IDLE_CONNS"
	envDatabaseConnLife     = EnvPrefix + "DATABASE_CONN_MAX_LIFETIME"
	envDatabaseConnIdle     = EnvPrefix + "DATABASE_CONN_MAX_IDLE_TIME"
)

// Env 是运行环境。
type Env string

const (
	EnvDev  Env = "dev"
	EnvTest Env = "test"
	EnvProd Env = "prod"
)

// Valid 报告 e 是否是已知的运行环境。
func (e Env) Valid() bool {
	switch e {
	case EnvDev, EnvTest, EnvProd:
		return true
	default:
		return false
	}
}

// LogLevel 是日志级别。
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// Valid 报告 l 是否是已知的日志级别。
func (l LogLevel) Valid() bool {
	switch l {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		return true
	default:
		return false
	}
}

// LogFormat 是日志输出格式。
type LogFormat string

const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// Valid 报告 f 是否是已知的日志格式。
func (f LogFormat) Valid() bool {
	switch f {
	case LogFormatJSON, LogFormatText:
		return true
	default:
		return false
	}
}

// Config 是服务的全部配置。
//
// yaml tag 必须与 configs/config.yaml 里的键**逐字对应**：
// 缺了 tag 时 yaml.v3 按字段名小写匹配（ReadTimeout → readtimeout），
// 而文件里写的是 read_timeout，结果是配置静默失效——值一直是默认值，最难查的一类问题。
type Config struct {
	App      App      `yaml:"app"`
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Log      Log      `yaml:"log"`
}

// App 是应用级标识。
type App struct {
	Name string `yaml:"name"`
	Env  Env    `yaml:"env"`
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
	Level  LogLevel  `yaml:"level"`
	Format LogFormat `yaml:"format"`
}

// Database 是 MySQL 连接参数。
// 密码只从环境变量读：配置文件会跟着 Git 走遍所有 clone，环境变量不会。
type Database struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	Name            string        `yaml:"name"`
	User            string        `yaml:"user"`
	Password        string        `yaml:"-" json:"-"` // ← 关键：不进 YAML，只从 env 读
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// LogValue 让结构化日志拿到分组字段（日志平台能按 database.host 检索），
// 并且密码只暴露为 ***。
func (d Database) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", d.Host),
		slog.Int("port", d.Port),
		slog.String("name", d.Name),
		slog.String("user", d.User),
		slog.String("password", "***"),
	)
}

// String 让 fmt 路径也脱敏：LogValue 覆盖 slog，String 覆盖 fmt.Printf / 错误拼接。
// 两个都要实现——只实现 LogValue 时，fmt.Printf("%v", d) 会明文打印密码。
//
// 注意：fmt.Sprintf("%#v", d) 会绕过 Stringer 打印 Go 语法，这是唯一拦不住的路径；
// 约定：不要用 %#v 打印配置。
func (d Database) String() string {
	// port 是 int，必须用 %d：写成 %s 会输出 "port=%!s(int=3306)"（fmt 的格式错误标记）。
	return fmt.Sprintf("Database{host=%s port=%d name=%s user=%s password=***}",
		d.Host, d.Port, d.Name, d.User)
}

// Default 返回内置默认值，即优先级最低的那一层。
//
// 契约：默认值与 configs/config.yaml 必须逐字段一致，由 TestConfigFileMatchesDefaults 钉死。
// 用带字段名的字面量而不是位置字面量：加字段时位置写法会静默错位。
func Default() *Config {
	return &Config{
		App: App{
			Name: "knowflow-apid",
			Env:  EnvDev,
		},
		Server: Server{
			Addr:            ":8080",
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Database: Database{
			Host:            "127.0.0.1",
			Port:            3306,
			Name:            "knowflow",
			User:            "knowflow",
			Password:        "",
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: 30 * time.Minute,
			ConnMaxIdleTime: 5 * time.Minute,
		},
		Log: Log{
			Level:  LogLevelInfo,
			Format: LogFormatJSON,
		},
	}
}

// Load 按「默认值 < 文件 < 环境变量」组装配置，并在返回前校验一次。
// path 为空表示只用默认值 + 环境变量（例如容器里全靠环境变量注入）。
//
// 三层是顺序执行的，不是二选一：path 只决定"文件这一层参不参与"，
// 环境变量这一层永远执行——所以传了 path，环境变量依然会覆盖文件里的值。
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

// applyFile 把 YAML 文件读进 cfg：只覆盖文件里出现的字段，其余保留默认值。
//
// 独立成函数（而不是塞进 Load）的两个理由：职责单一，以及测试能直接调它——
// TestConfigFileMatchesDefaults 必须绕开 Load，否则环境变量会混进来，测试结果就取决于跑测试的机器。
func applyFile(cfg *Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		// 两种错误要分开：不存在 → 给人"照着做"的提示；其它（权限等）→ 保留原始错误链
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("配置文件不存在 %s：请确认路径，或用环境变量 %s 指定另一个文件", path, EnvConfigFile)
		}
		return fmt.Errorf("打开配置文件 %s: %w", path, err)
	}
	// 只读文件：关闭失败没有数据可丢，忽略返回值是安全的
	defer f.Close()

	dec := yaml.NewDecoder(f)
	// 严格模式：YAML 里写错字段名必须报错，否则会"配置改了但没生效"
	dec.KnownFields(true)

	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("解析配置文件 %s: %w", path, err) // yaml 的错误自带行号
	}

	// 防静默忽略：Decode 只读第一份 YAML 文档，后面用 --- 分隔的内容会被无声丢弃。
	// 先解完 cfg 再检查"还有没有第二份"，顺序反过来会把第一份文档读进 extra、导致正常文件被误判。
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("配置文件 %s 含多份 YAML 文档（--- 分隔），只允许一份", path)
	}
	return nil
}

// applyEnv 用环境变量覆盖文件里的值。
//
// 用 LookupEnv 而不是 Getenv：能区分"没设置"和"显式设成空串"。
// 代价是 KF_LOG_LEVEL= 这种手滑会覆盖出一个空值——由 Validate 兜住（启动即失败，不会静默跑起来）。
// 每一项都显式列出，不做反射自动映射：配置项一多，反射就变成没人敢动的黑魔法。
func applyEnv(cfg *Config) error {
	if v, ok := os.LookupEnv(envAppEnv); ok {
		cfg.App.Env = Env(v)
	}
	if v, ok := os.LookupEnv(envServerAddr); ok {
		cfg.Server.Addr = v
	}
	if v, ok := os.LookupEnv(envServerShutdown); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			// 错误信息要带上"哪个变量、什么值"：只说 invalid duration，运维还得自己猜
			return fmt.Errorf("环境变量 %s=%q 不是合法时长（示例：15s）: %w", envServerShutdown, v, err)
		}
		cfg.Server.ShutdownTimeout = d
	}
	if v, ok := os.LookupEnv(envLogLevel); ok {
		cfg.Log.Level = LogLevel(v)
	}
	if v, ok := os.LookupEnv(envLogFormat); ok {
		cfg.Log.Format = LogFormat(v)
	}

	// ---- database ----
	// 每一项都单独解析、单独报错：错误消息点名"哪个变量、什么值、期望什么格式"。
	if v, ok := os.LookupEnv(envDatabaseHost); ok {
		cfg.Database.Host = v
	}
	if v, ok := os.LookupEnv(envDatabasePort); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("环境变量 %s=%q 不是合法端口（示例：3306）: %w", envDatabasePort, v, err)
		}
		cfg.Database.Port = n
	}
	if v, ok := os.LookupEnv(envDatabaseName); ok {
		cfg.Database.Name = v
	}
	if v, ok := os.LookupEnv(envDatabaseUser); ok {
		cfg.Database.User = v
	}
	if v, ok := os.LookupEnv(envDatabasePassword); ok {
		cfg.Database.Password = v
	}
	if v, ok := os.LookupEnv(envDatabaseMaxOpenConns); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("环境变量 %s=%q 不是合法整数（示例：10）: %w", envDatabaseMaxOpenConns, v, err)
		}
		cfg.Database.MaxOpenConns = n
	}
	if v, ok := os.LookupEnv(envDatabaseMaxIdleConns); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("环境变量 %s=%q 不是合法整数（示例：5）: %w", envDatabaseMaxIdleConns, v, err)
		}
		cfg.Database.MaxIdleConns = n
	}
	if v, ok := os.LookupEnv(envDatabaseConnLife); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("环境变量 %s=%q 不是合法时长（示例：30m）: %w", envDatabaseConnLife, v, err)
		}
		cfg.Database.ConnMaxLifetime = d
	}
	if v, ok := os.LookupEnv(envDatabaseConnIdle); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("环境变量 %s=%q 不是合法时长（示例：5m）: %w", envDatabaseConnIdle, v, err)
		}
		cfg.Database.ConnMaxIdleTime = d
	}

	return nil
}

// Validate 校验配置是否自洽。
//
// 为什么必须有它：配置有三个来源（代码默认值 / YAML / 环境变量），
// 其中环境变量这条路径完全绕过了 YAML 的类型检查，非法值只能在这里拦。
// 启动阶段宁可失败也不要带着错误配置跑起来——一个写错的 addr 会等到第一个请求才暴露，
// 那时你已经在排查业务问题了。
//
// 用 errors.Join 一次报出**全部**问题（而不是遇到第一个就返回）：改配置的人不该挤牙膏式地试错。
// 每条错误都点名具体字段和当前值，否则"哪个超时写错了"还得自己一个个看。
func (c *Config) Validate() error {
	var errs []error

	if c.App.Name == "" {
		errs = append(errs, errors.New("app.name 不能为空"))
	}
	if !c.App.Env.Valid() {
		errs = append(errs, fmt.Errorf("app.env=%q 非法，只允许 dev/test/prod", c.App.Env))
	}
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr 不能为空"))
	}
	// 四个超时分别报：挤在一条错误里，使用者不知道是哪一个写错了
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
	if !c.Log.Level.Valid() {
		errs = append(errs, fmt.Errorf("log.level=%q 非法，只允许 debug/info/warn/error", c.Log.Level))
	}
	if !c.Log.Format.Valid() {
		errs = append(errs, fmt.Errorf("log.format=%q 非法，只允许 json/text", c.Log.Format))
	}

	// ---- database ----
	// 这里只校验"值本身是否合法"，**绝不连库**：Validate 必须是纯函数，
	// 否则每个引用 config 的单测都得依赖一个跑着的 MySQL。
	// 也刻意不校验 Password 是否为空——那是部署环境的事（本地可能用 socket 认证、云上可能用 IAM），
	// 把它设成必填只会逼着所有不连库的测试去设环境变量。
	if c.Database.Host == "" {
		errs = append(errs, errors.New("database.host 不能为空"))
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		errs = append(errs, fmt.Errorf("database.port 必须在 1~65535 之间，当前 %d", c.Database.Port))
	}
	if c.Database.Name == "" {
		errs = append(errs, errors.New("database.name 不能为空"))
	}
	if c.Database.User == "" {
		errs = append(errs, errors.New("database.user 不能为空"))
	}
	if c.Database.MaxOpenConns < 1 {
		errs = append(errs, fmt.Errorf("database.max_open_conns 必须 ≥ 1，当前 %d", c.Database.MaxOpenConns))
	}
	// 跨字段不变量：单看每一项都合法，组合起来才错——只有这一层能拦。
	// 注意只在 max_open_conns 自身合法时才比：否则 max_open=0 会连带报出
	// "idle > open" 这条衍生错误，使用者得先分辨哪个才是根因。
	if c.Database.MaxIdleConns < 0 {
		errs = append(errs, fmt.Errorf("database.max_idle_conns 不能为负数，当前 %d", c.Database.MaxIdleConns))
	} else if c.Database.MaxOpenConns >= 1 && c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		errs = append(errs, fmt.Errorf("database.max_idle_conns(%d) 不能大于 max_open_conns(%d)",
			c.Database.MaxIdleConns, c.Database.MaxOpenConns))
	}
	// 0 在 database/sql 里是合法语义（不限制寿命），所以只拦负数
	if c.Database.ConnMaxLifetime < 0 {
		errs = append(errs, fmt.Errorf("database.conn_max_lifetime 不能为负数（0 表示不限制），当前 %s",
			c.Database.ConnMaxLifetime))
	}
	if c.Database.ConnMaxIdleTime < 0 {
		errs = append(errs, fmt.Errorf("database.conn_max_idle_time 不能为负数（0 表示不限制），当前 %s",
			c.Database.ConnMaxIdleTime))
	}

	// 注意：这里必须显式判空再 return nil。
	// fmt.Errorf("...%w", nil) 不会返回 nil，而是造出一个含 "%!w(<nil>)" 的假错误。
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("配置校验失败（%d 项）:\n%w", len(errs), errors.Join(errs...))
}
