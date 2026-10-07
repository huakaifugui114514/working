// Package respond 定义 HTTP 层的统一响应格式。
//
// 约定：
//   - 成功：HTTP 2xx + 业务数据 JSON（不套壳，资源本身就是响应体）
//   - 失败：HTTP 4xx/5xx + {"error":{"code":"...","message":"..."}}
//     code 给程序看（稳定、可枚举），message 给人看（可以改、可以本地化）
//
// 为什么要有统一格式：客户端需要一个稳定的错误结构才能写重试/提示逻辑；
// 每个 handler 各写各的（有的纯文本、有的 {"msg":...}），客户端就得为每个接口写一套解析。
//
// TODO(day2 · 你来实现)
package respond

import (
	"log/slog"
	"net/http"
)

// 错误码常量。code 一旦发布就不能随意改——客户端可能按它做分支。
const (
	CodeBadRequest = "bad_request"
	CodeNotFound   = "not_found"
	CodeInternal   = "internal_error"
)

// ErrorBody 是错误响应的外层结构。
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 是错误的具体内容。
// 注意：这里**没有** Cause 字段——内部原因（数据库报错、堆栈）只进日志，不出接口，
// 否则等于把内部结构送给攻击者。
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON 写一个 JSON 响应。
//
// 契约：
//   - 先设 Content-Type 与状态码，再 Encode body
//   - body 为 nil 时只写状态码，不写 body
//   - Encode 失败时响应已经开始发送（改不了状态码了），只能记日志；
//     注意 logger 可能为 nil（调用方允许不传），要判空
func JSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	panic("TODO(day2): 实现 JSON 响应")
}

// Error 写统一格式的错误响应（复用 JSON）。
func Error(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	panic("TODO(day2): 实现错误响应")
}
