// Package apperr 定义业务错误与错误码。
//
// 为什么需要它：知识库 CRUD 必然出现三类错误——参数不合法(400)、资源不存在(404)、
// 内部故障(500)。没有统一类型就会变成：
//   - 每个 handler 重复写 if errors.Is(err, sql.ErrNoRows) {...}
//   - 错误码字符串漂移（not_found / notFound / NotFound 迟早混用）
//   - service 层想告诉你"是 id=42 不存在"，handler 却只拿到一个 error，取不出来
//   - 5xx 日志里只剩一句"内部错误"，原因链断了
//
// 错误码由本包定义（领域语义），到 HTTP 状态码的映射由传输层负责——领域包不认识 HTTP。
package apperr

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidArgument = "invalid_argument" // → 400
	CodeNotFound        = "not_found"        // → 404，资源不存在
	CodeConflict        = "conflict"         // → 409，预留：并发/唯一性冲突
	CodeInternal        = "internal_error"   // → 500，兜底
)

// Error 是带错误码的业务错误。
type Error struct {
	Code    string // 稳定错误码，给程序看（如 "not_found"）
	Message string // 给人看的说明，允许改
	Cause   error  // 底层原因：只进日志，不出接口
}

// New 构造一个业务错误。
func New(code, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Cause:   nil,
	}
}

// Wrap 把一个底层错误包成业务错误，并保留原因链：
// 返回值的 Unwrap() 就是 err，所以 errors.Is / errors.As 能穿过这一层继续往下找。
func Wrap(err error, code, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Cause:   err,
	}
}

// Error 实现 error 接口，输出形如：code: not_found, message: 知识库不存在, cause: sql: no rows。
//
// 为什么把 Cause 一起输出：日志里要能一眼看到原因链，省掉在每一层手写 %v 的重复代码。
// 代价是内部信息会出现在 err.Error() 里——防线是响应体统一走 respond.Error（只写 code 与
// message），任何地方都不许把 err.Error() 直接塞进 HTTP 响应。
func (e *Error) Error() string {
	return fmt.Sprintf("code: %s, message: %s, cause: %v", e.Code, e.Message, e.Cause)
}

// Unwrap 让 errors.Is / errors.As 能穿过这一层。
func (e *Error) Unwrap() error {
	return e.Cause
}

// Code 取错误码，给 handler 与日志使用。三种情况：
//   - err == nil：返回空串，表示"没有错误"（调用方应先判 err != nil；空串不是合法的业务码）
//   - 链里找到 *apperr.Error：返回它的 Code。这里必须用 errors.As 而不是 err.(*Error) 类型断言，
//     因为被 %w 包装后最外层是 *fmt.wrapError，断言取不到里面的业务错误
//   - 其他情况（sql.ErrNoRows、context.DeadlineExceeded 等）：返回兜底码 CodeInternal，
//     语义是"未知错误按服务端故障处理"；code → HTTP 状态码的映射在传输层
func Code(err error) string {
	if err == nil {
		return ""
	}

	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}

	return CodeInternal
}
