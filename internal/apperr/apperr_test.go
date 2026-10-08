package apperr

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	e := New("not_found", "user not found")

	if e == nil {
		t.Fatal("New() returned nil")
	}
	if e.Code != "not_found" {
		t.Errorf("Code = %q, want not_found", e.Code)
	}
	if e.Message != "user not found" {
		t.Errorf("Message = %q", e.Message)
	}
	// New 的 Cause 是 nil
	if e.Cause != nil {
		t.Errorf("Cause = %q, want nil", e.Cause)
	}
}

func TestWrapPreservesCauseChain(t *testing.T) {
	base := errors.New("db connection refused")
	e := Wrap(base, "internal_error", "something broke")

	if e.Code != "internal_error" {
		t.Errorf("Code = %q", e.Code)
	}
	if e.Message != "something broke" {
		t.Errorf("Message = %q", e.Message)
	}
	// 关键契约：errors.Is 要能穿透业务错误到底层
	if !errors.Is(e, base) {
		t.Error("errors.Is(e, base) = false, want true")
	}
	// Unwrap 直接返回 Cause。
	// 这里要断言的是"同一个实例"，不是"链上找得到"，所以刻意用 != 而不是 errors.Is——
	// 换成一个包装过的错误也必须让这条失败（errorlint 的规则针对哨兵比较，此处豁免）。
	//nolint:errorlint // 见上：断言实例同一性
	if got := e.Unwrap(); got != base {
		t.Errorf("Unwrap() = %v, want %v", got, base)
	}
}

func TestWrapNilCause(t *testing.T) {
	// 虽然不推荐，但如果调用方传 nil，Unwrap 返回 nil，
	// errors.Is 也不该 panic
	e := Wrap(nil, "internal_error", "boom")
	if e.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", e.Unwrap())
	}
}

func TestErrorMessage(t *testing.T) {
	e := New("not_found", "user not found")
	got := e.Error()

	// 契约：输出包含 code 和 message
	if !strings.Contains(got, "not_found") {
		t.Errorf("Error() = %q, missing code", got)
	}
	if !strings.Contains(got, "user not found") {
		t.Errorf("Error() = %q, missing message", got)
	}
}

func TestCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil 返回空串",
			err:  nil,
			want: "",
		},
		{
			name: "直接业务错误",
			err:  New("not_found", "x"),
			want: "not_found",
		},
		{
			name: "一层 %w 包装",
			err:  fmt.Errorf("handler: %w", New("not_found", "x")),
			want: "not_found",
		},
		{
			name: "两层 %w 包装",
			err: fmt.Errorf("handler: %w",
				fmt.Errorf("service: %w", New("bad_request", "x"))),
			want: "bad_request",
		},
		{
			name: "Wrap 出来的业务错误",
			err:  Wrap(errors.New("db down"), "internal_error", "x"),
			want: "internal_error",
		},
		{
			name: "普通 error 走兜底码",
			err:  errors.New("plain"),
			want: CodeInternal,
		},
		{
			name: "sql.ErrNoRows 走兜底码",
			err:  sql.ErrNoRows,
			want: CodeInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Code(tt.err); got != tt.want {
				t.Errorf("Code(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// 专门验证 Code 用的是 errors.As 而不是类型断言。
// 如果实现换成 err.(*Error)，这个测试会挂。
func TestCodeMustUseErrorsAsNotTypeAssert(t *testing.T) {
	inner := New("not_found", "user not found")
	outer := fmt.Errorf("service: %w",
		fmt.Errorf("repo: %w", inner))

	if got := Code(outer); got != "not_found" {
		t.Errorf("Code(deeply wrapped) = %q, want not_found; "+
			"did you use type assertion instead of errors.As?", got)
	}
}

// 验证 errors.As 能从包装链里取出 *apperr.Error
func TestAsExtractsError(t *testing.T) {
	inner := New("not_found", "user not found")
	outer := fmt.Errorf("service: %w", inner)

	var e *Error
	if !errors.As(outer, &e) {
		t.Fatal("errors.As failed to extract *apperr.Error")
	}
	if e.Code != "not_found" {
		t.Errorf("Code = %q", e.Code)
	}
	if e.Message != "user not found" {
		t.Errorf("Message = %q", e.Message)
	}
}
