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
// 错误码按「谁产生」分两处定义，本包不做业务判断：
//   - 业务语义的码在 internal/apperr（service / repository 层产生）
//   - 传输层自己的码在本包（请求还没进入业务逻辑就失败，例如路由不存在）
//
// 本包负责两件事：渲染响应、把 code 映射成 HTTP 状态码。
// 映射放在这里而不是 apperr，是因为「HTTP」是协议细节——领域包不该认识它。
package respond

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/huakaifugui114514/working/internal/apperr"
)

// 传输层错误码：请求还没进入业务逻辑就失败的场景。
//
// 为什么和 apperr 的 not_found 分开：route_not_found（接口写错了）和 not_found
// （资源不存在）对客户端是两件完全不同的事——混用会让对方无法区分该改 URL 还是改参数，
// 排查日志时也分不清。code 一旦发布就不能随意改，客户端可能按它做分支。
const (
	CodeRouteNotFound = "route_not_found"
)

// DefaultInternalMessage 是 5xx 对外的固定文案：不透露任何内部信息。
const DefaultInternalMessage = "服务内部错误"

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
// 为什么先写 header 再 Encode：WriteHeader 一旦调用就不能再改状态码，
// 而 Encode 出错时我们已经无法回头改状态码了——顺序必须是"先定状态码，再尽力写 body"，
// 写失败只记日志（此时客户端多半已经断开）。
func JSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 响应已经开始发送，改不了状态码；logger 允许为 nil（调用方可以不传）
		if logger != nil {
			logger.Error("写 JSON 响应失败", "error", err, "status", status)
		}
	}
}

// Error 写一个统一格式的错误响应。
func Error(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	JSON(w, logger, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// StatusForCode 把错误码映射成 HTTP 状态码。
//
// 未登记的码一律按 500 处理：不能把没预期的码当 4xx 返回，否则等于把服务端 bug
// 伪装成客户端错误，监控上永远看不见。
func StatusForCode(code string) int {
	switch code {
	case apperr.CodeInvalidArgument:
		return http.StatusBadRequest
	case apperr.CodeUnauthorized:
		return http.StatusUnauthorized
	case apperr.CodeNotFound, CodeRouteNotFound:
		return http.StatusNotFound
	case apperr.CodeConflict:
		return http.StatusConflict
	case apperr.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// FromError 把任意 error 渲染成统一错误响应，handler 里一行就够：
//
//	if err != nil {
//		respond.FromError(w, s.logger, err)
//		return
//	}
//
// 为什么 4xx 和 5xx 要分开处理：
//   - 4xx 是「客户端能自己纠正的问题」，message 取业务错误的 Message 字段回显（那正是它的用途）
//   - 5xx 是「服务端自己的问题」，一律回固定文案；真实原因（可能含 SQL、内网地址）
//     只进日志——错误对象里带着的 Cause 绝不能出现在响应体里
func FromError(w http.ResponseWriter, logger *slog.Logger, err error) {
	if err == nil {
		// 调用方判断错了。这里不能静默返回 200：那会把 bug 藏起来，比报错更难查。
		if logger != nil {
			logger.Error("FromError 收到 nil error，调用方应先判断 err != nil")
		}
		Error(w, logger, http.StatusInternalServerError, apperr.CodeInternal, DefaultInternalMessage)
		return
	}

	code := apperr.Code(err)
	status := StatusForCode(code)

	var appErr *apperr.Error
	if errors.As(err, &appErr) && status < http.StatusInternalServerError {
		msg := appErr.Message
		if msg == "" {
			// 业务错误忘了写 message 时，别给客户端一个空字符串
			msg = "请求未被接受"
		}
		Error(w, logger, status, code, msg)
		return
	}

	// 走到这里有两种情况：明确的 5xx，或未登记的码（StatusForCode 已按 500 处理）。
	// 两种都按"服务端故障"对外表达：细节进日志，客户端只看到固定文案。
	if logger != nil {
		logger.Error("请求处理失败", "code", code, "status", status, "cause", err)
	}
	Error(w, logger, status, code, DefaultInternalMessage)
}
