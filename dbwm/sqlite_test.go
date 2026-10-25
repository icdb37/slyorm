package dbwm

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// fakeConn 是记录语句的连接替身：断言"执行了哪条 SQL"时用它，不碰真实数据库。
type fakeConn struct {
	mu       sync.Mutex
	stmts    []string
	args     [][]any
	affected int64
	lastID   int64
}

// ExecContext 记录执行的语句。
func (c *fakeConn) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stmts = append(c.stmts, query)
	c.args = append(c.args, args)
	return fakeResult{affected: c.affected, lastID: c.lastID}, nil
}

// QueryContext 替身不支持查询。
func (c *fakeConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("fakeConn: 不支持查询")
}

// QueryRowContext 替身不支持查询。
func (c *fakeConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// BeginTx 替身不支持事务。
func (c *fakeConn) BeginTx(context.Context) (Tx, error) {
	return nil, fmt.Errorf("fakeConn: 不支持事务")
}

// fakeResult 是 ExecContext 的返回结果。
type fakeResult struct {
	affected int64
	lastID   int64
}

func (r fakeResult) LastInsertId() (int64, error) { return r.lastID, nil }

func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

// tinyDialect 是参数上限很小的方言：用来验证批量插入按上限分块。
type tinyDialect struct{ ansi }

func (tinyDialect) Name() string { return "tiny" }

func (tinyDialect) MaxParams() int { return 3 }

// returningDialect 是带 RETURNING 子句的方言：用来验证"取回自增值"的那条路
// （PostgreSQL 的做法）。
type returningDialect struct{ ansi }

func (returningDialect) Name() string { return "returning" }

func (d returningDialect) Returning(column string) string {
	return " RETURNING " + QuoteIdent(d, column)
}

// ---------------------------------------------------------------------------
// 内存库
// ---------------------------------------------------------------------------

// newTestDB 开一个内存库并建好 person 表。
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const ddl = `CREATE TABLE person (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		age INTEGER,
		dept TEXT
	)`
	if _, err := db.conn.ExecContext(context.Background(), ddl); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// personRow 是 person 表的行目标。
type personRow struct {
	ID   int64
	Name string
	Age  int
	Dept string
	Cnt  int64 // 聚合别名 cnt 的落点
}

// ---------------------------------------------------------------------------
// slyorm 层的模拟：泛型包装在调用侧（本包保持非泛型）
// ---------------------------------------------------------------------------

// personTargets 给出 *personRow 各列的扫描地址（顺序与列清单一致）。
func personTargets(columns ...string) func(*personRow) []any {
	return func(p *personRow) []any {
		out := make([]any, 0, len(columns))
		for _, c := range columns {
			switch c {
			case "id":
				out = append(out, &p.ID)
			case "name":
				out = append(out, &p.Name)
			case "age":
				out = append(out, &p.Age)
			case "dept":
				out = append(out, &p.Dept)
			case "cnt":
				out = append(out, &p.Cnt)
			default:
				panic("personTargets: 未知列 " + c)
			}
		}
		return out
	}
}

// factoryOf 把泛型收集闭包适配成 RowFactory：NewRow 时创建 *T、收集进
// 列表、返回字段地址 —— 扫描完成的行对象因此直接落在 rows 里。
func factoryOf[T any](rows *[]*T, targets func(*T) []any) RowFactory {
	return RowFactoryFunc(func() []any {
		t := new(T)
		*rows = append(*rows, t)
		return targets(t)
	})
}

// collectAll 模拟 slyorm 层的泛型全量查询：业务侧直接拿到 []*T。
func collectAll[T any](ctx context.Context, db *DB, q *Query, targets func(*T) []any) ([]*T, error) {
	var rows []*T
	if err := db.Qryall(ctx, q, factoryOf(&rows, targets)); err != nil {
		return nil, err
	}
	return rows, nil
}

// collectOne 模拟 slyorm 层的泛型单行查询。
func collectOne[T any](ctx context.Context, db *DB, q *Query, targets func(*T) []any) ([]*T, error) {
	var rows []*T
	if err := db.Qryone(ctx, q, factoryOf(&rows, targets)); err != nil {
		return nil, err
	}
	return rows, nil
}

// collectPgs 模拟 slyorm 层的泛型分页查询：本页行 + 总数。
func collectPgs[T any](ctx context.Context, db *DB, q *PagedQuery, targets func(*T) []any) ([]*T, int64, error) {
	var rows []*T
	total, err := db.Qrypgs(ctx, q, factoryOf(&rows, targets))
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// insertPeople 往 person 表里塞几行，返回插入的自增值。
func insertPeople(t *testing.T, db *DB, names ...string) {
	t.Helper()
	rows := make([][]any, 0, len(names))
	for _, n := range names {
		rows = append(rows, []any{n, 30, "dev"})
	}
	if _, err := db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"name", "age", "dept"},
		Rows:    rows,
	}); err != nil {
		t.Fatalf("插入失败: %v", err)
	}
}
