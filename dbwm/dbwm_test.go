package dbwm

import (
	"context"
	"database/sql"
	"testing"
)

// ---------------------------------------------------------------------------
// R9 句柄与构造入口
// ---------------------------------------------------------------------------

func TestOpenPicksDialectByDriver(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if db.Dialect().Name() != ANSI.Name() {
		t.Fatalf("方言 = %s，期望 %s", db.Dialect().Name(), ANSI.Name())
	}
}

func TestOpenUnknownDriverFails(t *testing.T) {
	_, err := Open("nosuchdriver", ":memory:")
	wantErr(t, err, "驱动")
}

func TestNewSQLKeepsConnectionOwnership(t *testing.T) {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	db := NewSQL(base)
	if db.Dialect().Name() != ANSI.Name() {
		t.Fatalf("方言 = %s，期望 %s", db.Dialect().Name(), ANSI.Name())
	}
	// 注入的连接所有权在注入方：Close 不代关，连接仍可用。
	if err := db.Close(); err != nil {
		t.Fatalf("关闭句柄失败: %v", err)
	}
	if _, err := base.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("注入的连接不该被关闭: %v", err)
	}
}
