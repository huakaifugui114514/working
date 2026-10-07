// Package config 负责加载、覆盖与校验服务配置。
//
// 规则只有一条，而且是可见的：内置默认值 < YAML 文件 < 环境变量（KF_ 前缀）。
// 为什么不用 viper 这类配置框架：隐式行为太多（自动读环境变量、自动监听文件、键名大小写规则），
// 出问题时很难回答"这个值到底是从哪来的"。
//
// 为什么密钥只能从环境变量读：配置文件会跟着 Git 走遍所有 clone，环境变量不会。
//
// TODO(day2 · 你来实现)。开工前先跑一条命令（这是你已同意过的依赖）：
//
//	go get gopkg.in/yaml.v3@v3.0.1
package config

import "time"

// 环境变量与路径约定——常量已定好，直接引用即可。
const (
	// EnvPrefix 是所有覆盖项的前缀，避免与其他程序的变量重名。
	EnvPrefix = "KF_"
	// EnvConfigFile 指定配置文件路径。它属于"启动参数"，不是 Config 的字段，
	// 所以由 main 读取后传给 Load。
	EnvConfigFile = EnvPrefix + "CONFIG_FILE"
	// DefaultPath 是未显式指定配置文件时使用的路径（相对进程工作目录）。
	DefaultPath = "configs/config.yaml"
)

// Config 是服务的全部配置。
//
// TODO(day2): 补 yaml tag。**必须补**，而且要补对：yaml.v3 在没有 tag 时按字段名小写匹配，
// ReadTimeout 会去找 readtimeout，而 configs/config.yaml 里写的是 read_timeout——
// 结果是配置静默失效（值一直是默认值），这类问题最难查。
type Config struct {
	App    App
	Server Server
	Log    Log
}

// App 是应用级标识。
type App struct {
	Name string
	Env  string // dev / test / prod
}

// Server 是 HTTP 服务端参数。
// 这几个超时不是"可选项"：少了它们，一个慢客户端就能一直占着连接，把服务拖垮。
type Server struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// Log 是日志参数。
type Log struct {
	Level  string
	Format string
}

// Default 返回内置默认值，即优先级最低的那一层。
//
// 契约：默认值必须与 configs/config.yaml 保持一致（:8080 / 5s / 10s / 60s / 15s / info / json）。
func Default() *Config {
	panic("TODO(day2): 返回默认配置")
}

// Load 按「默认值 < 文件 < 环境变量」组装配置，并在返回前校验一次。
// path 为空表示只用默认值 + 环境变量（例如容器里全靠环境变量注入）。
//
// 契约（实现时逐条满足）：
//   - 文件不存在 / 解析失败 → 返回错误，并带上出错的文件路径
//   - YAML 里出现未知字段 → 必须报错（提示：yaml.NewDecoder 的 KnownFields(true)），
//     否则写错的字段名会被静默忽略，表现为"配置改了但没生效"
//   - 环境变量只覆盖显式列出的几项：KF_APP_ENV、KF_SERVER_ADDR、
//     KF_SERVER_SHUTDOWN_TIMEOUT、KF_LOG_LEVEL、KF_LOG_FORMAT
//     （不要用反射自动映射：配置项一多就变成没人敢动的黑魔法）
//   - 环境变量解析失败必须返回错误，不能忽略（静默失败最难排查）
//   - 最后调用 Validate，把校验错误原样返回
func Load(path string) (*Config, error) {
	panic("TODO(day2): 实现配置加载")
}

// Validate 校验配置是否自洽。
//
// 契约：启动阶段宁可失败也不要带着错误配置跑起来——一个写错的 addr 会等到第一个请求
// 才暴露，那时你已经在排查业务问题了。
// 要求：一次报出**全部**问题，而不是遇到第一个就返回（提示：errors.Join）。
// 校验项：app.name 非空、app.env ∈ {dev,test,prod}、server.addr 非空、
// 四个超时都 > 0、log.level ∈ {debug,info,warn,error}、log.format ∈ {json,text}。
func (c *Config) Validate() error {
	panic("TODO(day2): 实现配置校验")
}
