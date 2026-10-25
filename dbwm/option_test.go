package dbwm

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// R9 构造选项
// ---------------------------------------------------------------------------

func TestWithDialectSetsDialect(t *testing.T) {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	db := NewSQL(base, WithDialect(MySQL))
	if db.Dialect().Name() != MySQL.Name() {
		t.Fatalf("方言 = %s，期望 %s", db.Dialect().Name(), MySQL.Name())
	}
}

func TestNoDialectOptionUsesANSI(t *testing.T) {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	if got := New(&fakeConn{}).Dialect().Name(); got != ANSI.Name() {
		t.Fatalf("New 的方言 = %s，期望 %s", got, ANSI.Name())
	}
	if got := NewSQL(base).Dialect().Name(); got != ANSI.Name() {
		t.Fatalf("NewSQL 的方言 = %s，期望 %s", got, ANSI.Name())
	}
}

func TestOpenDialectOptionBeatsDriverMapping(t *testing.T) {
	db, err := Open("sqlite", ":memory:", WithDialect(PostgreSQL))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if db.Dialect().Name() != PostgreSQL.Name() {
		t.Fatalf("方言 = %s，期望 %s", db.Dialect().Name(), PostgreSQL.Name())
	}
}

func TestNilOptionIsIgnored(t *testing.T) {
	db := New(&fakeConn{}, nil)
	if db.Dialect().Name() != ANSI.Name() {
		t.Fatalf("方言 = %s，期望 %s", db.Dialect().Name(), ANSI.Name())
	}
}
