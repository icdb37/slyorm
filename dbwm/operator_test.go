package dbwm

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// R10 算子扩展
// ---------------------------------------------------------------------------

func TestCustomOperator(t *testing.T) {
	if err := RegisterOperator("contains", func(d Dialect, c *Wcond) (string, []any, error) {
		return QuoteIdent(d, c.Column) + " LIKE ?", []any{"%" + fmt.Sprint(c.Arg) + "%"}, nil
	}); err != nil {
		t.Fatalf("注册算子失败: %v", err)
	}
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("note", "contains", "x")),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person" WHERE "note" LIKE ?`, []any{"%x%"})
}

// 自定义算子片段里的 ? 换成渲染器分配的占位符：编号在整条语句里连续。
func TestCustomOperatorPlaceholderIsNumbered(t *testing.T) {
	if err := RegisterOperator("numlike", func(d Dialect, c *Wcond) (string, []any, error) {
		return QuoteIdent(d, c.Column) + " LIKE ?", []any{"%" + fmt.Sprint(c.Arg) + "%"}, nil
	}); err != nil {
		t.Fatalf("注册算子失败: %v", err)
	}
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("note", "numlike", "x"), cond("age", OpGt, 30)),
	})
	stmt, args, err := renderQuery(PostgreSQL, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "id" FROM "person" WHERE "note" LIKE $1 AND "age" > $2`,
		[]any{"%x%", 30})
}

func TestRegisterOperatorRejectsDuplicateName(t *testing.T) {
	op := func(d Dialect, c *Wcond) (string, []any, error) {
		return QuoteIdent(d, c.Column) + " = ?", []any{c.Arg}, nil
	}
	if err := RegisterOperator("eq", op); err == nil {
		t.Fatalf("内置算子不该被覆盖")
	}
	if err := RegisterOperator("dup", op); err != nil {
		t.Fatalf("注册算子失败: %v", err)
	}
	if err := RegisterOperator("dup", op); err == nil {
		t.Fatalf("已注册的算子不该被覆盖")
	}
}

func TestRegisterOperatorRejectsInvalidInput(t *testing.T) {
	if err := RegisterOperator("", func(d Dialect, c *Wcond) (string, []any, error) { return "", nil, nil }); err == nil {
		t.Fatalf("空算子名该报错")
	}
	if err := RegisterOperator("nilop", nil); err == nil {
		t.Fatalf("nil 算子实现该报错")
	}
}

func TestCustomOperatorInHavingRewritesAggAlias(t *testing.T) {
	if err := RegisterOperator("cntlike", func(d Dialect, c *Wcond) (string, []any, error) {
		return QuoteIdent(d, c.Column) + " > ?", []any{c.Arg}, nil
	}); err != nil {
		t.Fatalf("注册算子失败: %v", err)
	}
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Having:  group(CombAnd, cond("cnt", "cntlike", 10)),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept" HAVING COUNT(*) > ?`,
		[]any{10})
}
