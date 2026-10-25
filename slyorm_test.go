package slyorm

import (
	"database/sql"
	"testing"

	"github.com/icdb37/slyorm/dbwm"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// R1 句柄构造：方言走构造选项
// ---------------------------------------------------------------------------

func TestNewSQLDialectOption(t *testing.T) {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	if got := NewSQL(base).Dialect().Name(); got != dbwm.ANSI.Name() {
		t.Fatalf("不给选项的方言 = %s，期望 %s", got, dbwm.ANSI.Name())
	}
	if got := NewSQL(base, dbwm.WithDialect(dbwm.MySQL)).Dialect().Name(); got != dbwm.MySQL.Name() {
		t.Fatalf("带选项的方言 = %s，期望 %s", got, dbwm.MySQL.Name())
	}
}
