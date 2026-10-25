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
// R9 事务
// ---------------------------------------------------------------------------

func TestTxCommits(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.Tx(ctx, func(ctx context.Context) error {
		_, err := db.Insert(ctx, &Insert{
			Table:   "person",
			Columns: []string{"name"},
			Rows:    [][]any{{"张三"}},
		})
		return err
	})
	if err != nil {
		t.Fatalf("事务失败: %v", err)
	}
	rows, err := collectAll(ctx, db, &Query{Table: "person", Columns: []string{"name"}},
		personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("事务外查到 %d 行，期望 1 行", len(rows))
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	sentinel := errors.New("业务失败")
	err := db.Tx(ctx, func(ctx context.Context) error {
		if _, err := db.Insert(ctx, &Insert{
			Table:   "person",
			Columns: []string{"name"},
			Rows:    [][]any{{"张三"}},
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v，期望原样返回业务错误", err)
	}
	rows, err := collectAll(ctx, db, &Query{Table: "person", Columns: []string{"name"}},
		personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("回滚后查到 %d 行，期望 0 行", len(rows))
	}
}

func TestTxPanicRollsBackAndRepanics(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Fatalf("期望 panic 被抛回")
			}
		}()
		_ = db.Tx(ctx, func(ctx context.Context) error {
			_, _ = db.Insert(ctx, &Insert{
				Table:   "person",
				Columns: []string{"name"},
				Rows:    [][]any{{"张三"}},
			})
			panic("boom")
		})
	}()
	rows, err := collectAll(ctx, db, &Query{Table: "person", Columns: []string{"name"}},
		personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("回滚后查到 %d 行，期望 0 行", len(rows))
	}
}

func TestNestedTxReusesOuterTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.Tx(ctx, func(ctx context.Context) error {
		// 内层返回错误：不该回滚外层事务（提交与回滚都归最外层）。
		inner := db.Tx(ctx, func(ctx context.Context) error {
			if _, err := db.Insert(ctx, &Insert{
				Table:   "person",
				Columns: []string{"name"},
				Rows:    [][]any{{"张三"}},
			}); err != nil {
				return err
			}
			return errors.New("内层失败")
		})
		if inner == nil {
			t.Fatalf("内层事务该返回错误")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("事务失败: %v", err)
	}
	rows, err := collectAll(ctx, db, &Query{Table: "person", Columns: []string{"name"}},
		personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("查到 %d 行，期望 1 行（嵌套事务不该回滚外层）", len(rows))
	}
}

func TestTxEndedCtxFails(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var inner context.Context
	if err := db.Tx(ctx, func(c context.Context) error {
		inner = c
		return nil
	}); err != nil {
		t.Fatalf("事务失败: %v", err)
	}
	// 事务已经结束：用它的 ctx 再查必须报错，不能静默改走连接池。
	_, err := collectAll(inner, db, &Query{Table: "person", Columns: []string{"name"}},
		personTargets("name"))
	if err == nil {
		t.Fatalf("事务结束后的 ctx 查询该报错")
	}
	wantErr(t, err, "事务已结束")
}

// errRollbackTx 是回滚必失败的事务替身。
type errRollbackTx struct{}

func (errRollbackTx) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, fmt.Errorf("errRollbackTx: 不该执行语句")
}

func (errRollbackTx) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("errRollbackTx: 不该查询")
}

func (errRollbackTx) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (errRollbackTx) Commit() error { return nil }

func (errRollbackTx) Rollback() error { return errors.New("回滚失败") }

// errRollbackConn 开事务必成功，返回的事务回滚必失败。
type errRollbackConn struct{}

func (errRollbackConn) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, fmt.Errorf("errRollbackConn: 不该执行语句")
}

func (errRollbackConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("errRollbackConn: 不该查询")
}

func (errRollbackConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func (errRollbackConn) BeginTx(context.Context) (Tx, error) {
	return errRollbackTx{}, nil
}

// TestTxRollbackFailureKeepsBizError 验证回滚失败时业务错误不被吞：
// R6.4 要求"fn 返回错误 → 回滚并原样返回该错误"，两个错误都该可见，
// 且 errors.Is 对业务错误可穿透。
func TestTxRollbackFailureKeepsBizError(t *testing.T) {
	db := New(errRollbackConn{}, WithDialect(ANSI))
	sentinel := errors.New("业务失败")
	err := db.Tx(context.Background(), func(context.Context) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v，业务错误该被保留（errors.Is 可穿透）", err)
	}
	if !strings.Contains(err.Error(), "回滚事务失败") {
		t.Fatalf("错误 = %v，该带上回滚失败的原因", err)
	}
}
