// Package respond 定义 HTTP 层的统一响应格式。
//
// 为什么要有"统一响应体"：客户端需要一个稳定的错误结构才能写重试/提示逻辑。
// 如果每个 handler 各写各的（有的返回纯文本、有的返回 {"msg": ...}），
// 客户端就得为每个接口写一套解析代码。
//
// 约定：
//   - 成功：HTTP 2xx + 业务数据 JSON（不套壳，RESTful 的资源本身就是响应体）
//   - 失败：HTTP 4xx/5xx + {"error":{"code":"...","message":"..."}}
//     code 是给程序看的（稳定、可枚举），message 是给人看的（可改、可本地化）
package respond

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// 错误码常量。code 一旦发布就不能随意改——客户端可能按它做分支。
const (
	CodeBadRequest   = "bad_request"
	CodeNotFound     = "not_found"
	CodeInternal     = "internal_error"
	CodeUnauthorized = "unauthorized"
)

// ErrorBody 是错误响应的外层结构。
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 是错误的具体内容。
// 注意：这里没有 Cause 字段——内部原因（数据库报错、堆栈）只进日志，不出接口，
// 否则等于把内部结构送给攻击者。
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON 写一个 JSON 响应。
//
// 为什么先写 header 再 Encode：WriteHeader 一旦调用就不能再改状态码，
// 而 json.Encoder 出错时我们已经无法回头改状态码了——所以顺序必须是"先定状态码，
// 再尽力写 body"，写失败只记日志（此时客户端多半已经断开）。
func JSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 响应已经开始发送，无法再改状态码；记录到日志即可。
		if logger != nil {
			logger.Error("写 JSON 响应失败", "error", err, "status", status)
		}
	}
}

// Error 写一个统一格式的错误响应。
func Error(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	JSON(w, logger, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}
