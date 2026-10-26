package dbwm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R11 脚本执行（ExecScript）
// ---------------------------------------------------------------------------

// TestExecScriptRuns 验证脚本原样执行：多语句整发、效果落库。
func TestExecScriptRuns(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const script = `CREATE TABLE t (v TEXT NOT NULL);
INSERT INTO t (v) VALUES ('a'), ('b');`
	if err := db.ExecScript(context.Background(), script); err != nil {
		t.Fatalf("执行脚本失败: %v", err)
	}
	var n int
	if err := db.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("脚本效果落库 %d 行，期望 2 行", n)
	}
}

// TestExecScriptBlankNoop 验证空白文本不发语句（fakeConn 记录零条语句）。
func TestExecScriptBlankNoop(t *testing.T) {
	conn := &fakeConn{}
	db := New(conn, WithDialect(ANSI))
	for _, blank := range []string{"", "   ", "\n\t  \n"} {
		if err := db.ExecScript(context.Background(), blank); err != nil {
			t.Fatalf("空白文本 %q 不该报错: %v", blank, err)
		}
	}
	if len(conn.stmts) != 0 {
		t.Fatalf("空白文本不该发语句，实际发了 %d 条", len(conn.stmts))
	}
}

// txRecordConn 是记录语句的连接与事务替身：断言语句走没走事务。
type txRecordConn struct {
	connStmts []string
	txStmts   []string
}

func (c *txRecordConn) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	c.connStmts = append(c.connStmts, q)
	return fakeResult{}, nil
}

func (c *txRecordConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("txRecordConn: 不支持查询")
}

func (c *txRecordConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (c *txRecordConn) BeginTx(context.Context) (Tx, error) {
	return txRecordTx{c}, nil
}

// txRecordTx 把执行记到所属连接的 txStmts 上。
type txRecordTx struct{ c *txRecordConn }

func (t txRecordTx) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	t.c.txStmts = append(t.c.txStmts, q)
	return fakeResult{}, nil
}

func (t txRecordTx) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("txRecordTx: 不支持查询")
}

func (t txRecordTx) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (t txRecordTx) Commit() error   { return nil }
func (t txRecordTx) Rollback() error { return nil }

// TestExecScriptInTx 验证 ctx 带事务时语句在事务内执行（不走连接池）。
func TestExecScriptInTx(t *testing.T) {
	conn := &txRecordConn{}
	db := New(conn, WithDialect(ANSI))
	err := db.Tx(context.Background(), func(ctx context.Context) error {
		return db.ExecScript(ctx, "CREATE TABLE t (v TEXT)")
	})
	if err != nil {
		t.Fatalf("事务内执行脚本失败: %v", err)
	}
	if len(conn.txStmts) != 1 {
		t.Fatalf("语句应记在事务上，实际 %d 条", len(conn.txStmts))
	}
	if len(conn.connStmts) != 0 {
		t.Fatalf("事务内语句不该走连接池，实际 %d 条", len(conn.connStmts))
	}
}

// errScriptConn 是执行必失败的连接替身。
type errScriptConn struct{ txRecordConn }

func (c *errScriptConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errScriptFail
}

// errScriptFail 是驱动失败的哨兵错误。
var errScriptFail = errors.New("驱动失败")

// TestExecScriptErrPassthrough 验证驱动错误可穿透、错误不带脚本正文，
// 且类别可判定（prd-error R3.4：是数据库错误，归 KindOther）。
func TestExecScriptErrPassthrough(t *testing.T) {
	db := New(&errScriptConn{}, WithDialect(ANSI))
	script := "CREATE TABLE very_long_table_name (" + strings.Repeat("c", 200) + " INT)"
	err := db.ExecScript(context.Background(), script)
	if !errors.Is(err, errScriptFail) {
		t.Fatalf("错误 = %v，期望 errors.Is 穿透驱动错误", err)
	}
	if !strings.Contains(err.Error(), "执行脚本失败") {
		t.Fatalf("错误 = %v，该带中文说明", err)
	}
	if strings.Contains(err.Error(), "very_long_table_name") {
		t.Fatalf("错误不该带脚本正文: %v", err)
	}
	if got := KindOf(err); got != KindOther {
		t.Fatalf("类别 = %v，期望 %v", got, KindOther)
	}
	if !errors.Is(err, ErrDatabase) {
		t.Fatalf("错误 = %v，期望是数据库错误", err)
	}
}
