// Package version 提供构建期注入的版本信息。
//
// 为什么不直接在 main 里写常量：版本号必须由构建流程注入（Makefile 里的
// -ldflags "-X"），代码里不留"手改版本号"的口子——手改的版本号迟早会忘记改，
// 而线上排查第一个要问的就是"这个进程跑的是哪个 commit"。
package version

// 这三个变量在构建时被 -ldflags 覆盖；默认值只会在 `go run` 时出现。
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

// Info 是一次构建的完整标识，用于启动日志与 /healthz 响应。
// 字段带 json tag：它会被直接序列化进接口响应，不需要在每个 handler 里手拼。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

// Get 返回当前二进制的版本信息。
func Get() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime}
}
