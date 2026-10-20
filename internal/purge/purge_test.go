package purge

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{name: "默认值", opts: Options{Retention: DefaultRetention, BatchSize: DefaultBatchSize, Interval: DefaultInterval}},
		{name: "retention 为 0", opts: Options{Retention: 0, BatchSize: 10}, wantErr: true},
		{name: "retention 为负", opts: Options{Retention: -time.Hour, BatchSize: 10}, wantErr: true},
		{name: "batch 为 0", opts: Options{Retention: time.Hour, BatchSize: 0}, wantErr: true},
		{name: "batch 为负", opts: Options{Retention: time.Hour, BatchSize: -1}, wantErr: true},
		{name: "batch 超过上限", opts: Options{Retention: time.Hour, BatchSize: MaxBatchSize + 1}, wantErr: true},
		{name: "interval 为负", opts: Options{Retention: time.Hour, BatchSize: 10, Interval: -time.Second}, wantErr: true},
		{name: "interval 为 0 合法", opts: Options{Retention: time.Hour, BatchSize: 10, Interval: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewRunnerRejectsNil(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name string
		db   DB
		log  *slog.Logger
	}{
		{name: "db nil", db: nil, log: logger},
		{name: "logger nil", db: failingDB{t: t}, log: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewRunner 应当 panic")
				}
			}()
			NewRunner(tt.db, tt.log)
		})
	}
}

func TestSleep(t *testing.T) {
	if err := sleep(context.Background(), 0); err != nil {
		t.Fatalf("0 时长不应睡: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled, got %v", err)
	}
}

// 这个测试只钉住 Run 在参数校验阶段失败时不碰数据库。
func TestRunRejectsInvalidOptionsBeforeDB(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewRunner(failingDB{t: t}, logger)
	_, err := runner.Run(context.Background(), Options{})
	if err == nil {
		t.Fatal("非法参数应返回错误")
	}
	if !strings.Contains(err.Error(), "retention") {
		t.Fatalf("错误消息应指出 retention，got %v", err)
	}
}

// failingDB 的每个方法都让测试失败：参数校验必须在访问 DB 之前完成。
type failingDB struct {
	t *testing.T
}

func (f failingDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	f.t.Fatal("不应访问数据库")
	return nil, nil
}

func (f failingDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	f.t.Fatal("不应访问数据库")
	return nil, nil
}

func (f failingDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	f.t.Fatal("不应访问数据库")
	return nil
}

func (f failingDB) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	f.t.Fatal("不应访问数据库")
	return nil, nil
}
