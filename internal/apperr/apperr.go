// Package apperr 定义业务错误与错误码。
//
// 为什么需要它：Day 4 的 CRUD 里必然出现三类错误——参数不合法(400)、资源不存在(404)、
// 内部故障(500)。没有统一类型就会变成：
//   - 每个 handler 重复写 if errors.Is(err, sql.ErrNoRows) {...}
//   - 错误码字符串漂移（not_found / notFound / NotFound 迟早混用）
//   - service 层想告诉你"是 id=42 不存在"，handler 却只拿到一个 error，取不出来
//   - 5xx 日志里只剩一句"内部错误"，原因链断了
//
// TODO(day2 · 你来实现)。热身建议：先做完 labs/w1/05（errors.Is / errors.As / %w），
// 这两个是同一个知识点。
package apperr

// Error 是带错误码的业务错误。
type Error struct {
	Code    string // 稳定错误码，给程序看（如 "not_found"）
	Message string // 给人看的说明，允许改
	Cause   error  // 底层原因：只进日志，不出接口
}

// New 构造一个业务错误。
func New(code, message string) *Error {
	panic("TODO(day2): 实现 New")
}

// Wrap 把一个底层错误包成业务错误（保留原因链）。
//
// 契约：返回的 *Error 必须让 errors.Is / errors.As 能**穿透**它继续往下找 Cause。
func Wrap(err error, code, message string) *Error {
	panic("TODO(day2): 实现 Wrap")
}

// Error 实现 error 接口。
//
// 契约：输出里要包含 code 与 message；Cause 要不要拼进去由你决定——
// 但请说清两种选择的代价（提示：日志里能不能看到原因链 vs 会不会把内部信息带出去）。
func (e *Error) Error() string {
	panic("TODO(day2): 实现 Error()")
}

// Unwrap 让 errors.Is / errors.As 能穿过这一层。
func (e *Error) Unwrap() error {
	panic("TODO(day2): 实现 Unwrap()")
}

// Code 取错误码，给 handler/日志用。
//
// 契约：
//   - 能穿透任意层 %w 包装后仍然取到业务错误（思考：为什么这里必须用 errors.As，
//     而不能写 err.(*Error) 类型断言？）
//   - 传入 nil 不能 panic；传入普通 error（如 sql.ErrNoRows）返回兜底码
//   - 兜底码用哪个常量、映射到哪个 HTTP 状态码，由你决定并在注释里写清理由
func Code(err error) string {
	panic("TODO(day2): 实现 Code()")
}
