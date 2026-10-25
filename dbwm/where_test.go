package dbwm

import (
	"testing"
)

// ---------------------------------------------------------------------------
// R3 条件渲染与括号规则
// ---------------------------------------------------------------------------

func TestTopLevelGroupHasNoOuterBracket(t *testing.T) {
	cases := []struct {
		name string
		g    *Wnode
		want string
	}{
		{
			name: "and 组",
			g:    group(CombAnd, cond("a", OpEq, 1), cond("b", OpEq, 2)),
			want: `"a" = ? AND "b" = ?`,
		},
		{
			name: "or 组",
			g:    group(CombOr, cond("a", OpEq, 1), cond("b", OpEq, 2)),
			want: `"a" = ? OR "b" = ?`,
		},
		{
			name: "not 组",
			g:    group(CombNot, cond("a", OpEq, 1), cond("b", OpEq, 2)),
			want: `NOT ("a" = ? AND "b" = ?)`,
		},
		{
			name: "连接方式为空按 and 处理",
			g:    group("", cond("a", OpEq, 1), cond("b", OpEq, 2)),
			want: `"a" = ? AND "b" = ?`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := paged(Query{Table: "person", Columns: []string{"id"}, Where: c.g})
			stmt, args, err := renderQuery(ANSI, q)
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			want := `SELECT "id" FROM "person" WHERE ` + c.want
			assertSQL(t, stmt, args, want, []any{1, 2})
		})
	}
}

func TestNestedGroupIsWrapped(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where: group(CombOr,
			cond("a", OpEq, 1),
			group(CombAnd, cond("b", OpEq, 2), cond("c", OpEq, 3)),
		),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "id" FROM "person" WHERE "a" = ? OR ("b" = ? AND "c" = ?)`,
		[]any{1, 2, 3})
}

func TestSingleConditionGroupStillWrapped(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, group(CombAnd, cond("a", OpEq, 1))),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person" WHERE ("a" = ?)`, []any{1})
}

func TestEmptyGroupDoesNotParticipate(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where: group(CombAnd,
			cond("a", OpEq, 1),
			group(CombOr),
		),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person" WHERE "a" = ?`, []any{1})
}

func TestEmptyTopLevelGroupDropsWhere(t *testing.T) {
	q := paged(Query{Table: "person", Columns: []string{"id"}, Where: group(CombAnd)})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person"`, nil)
}

func TestParamOrderFollowsTreeOrder(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where: group(CombAnd,
			cond("a", OpEq, 1),
			group(CombOr, cond("b", OpEq, 2), cond("c", OpEq, 3)),
			cond("d", OpEq, 4),
		),
	})
	_, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	for i, want := range []any{1, 2, 3, 4} {
		if args[i] != want {
			t.Fatalf("第 %d 个参数 = %v，期望 %v", i, args[i], want)
		}
	}
}

func TestOperatorRendering(t *testing.T) {
	cases := []struct {
		name     string
		c        *Wcond
		want     string
		wantArgs []any
	}{
		{name: "eq", c: &Wcond{Column: "a", Op: OpEq, Arg: 1}, want: `"a" = ?`, wantArgs: []any{1}},
		{name: "ne", c: &Wcond{Column: "a", Op: OpNe, Arg: 1}, want: `"a" <> ?`, wantArgs: []any{1}},
		{name: "gt", c: &Wcond{Column: "a", Op: OpGt, Arg: 1}, want: `"a" > ?`, wantArgs: []any{1}},
		{name: "gte", c: &Wcond{Column: "a", Op: OpGte, Arg: 1}, want: `"a" >= ?`, wantArgs: []any{1}},
		{name: "lt", c: &Wcond{Column: "a", Op: OpLt, Arg: 1}, want: `"a" < ?`, wantArgs: []any{1}},
		{name: "lte", c: &Wcond{Column: "a", Op: OpLte, Arg: 1}, want: `"a" <= ?`, wantArgs: []any{1}},
		{name: "like", c: &Wcond{Column: "a", Op: OpLike, Arg: "x"}, want: `"a" LIKE ?`, wantArgs: []any{"x"}},
		{name: "in", c: &Wcond{Column: "a", Op: OpIn, Arg: []any{1, 2}}, want: `"a" IN (?,?)`, wantArgs: []any{1, 2}},
		{name: "in 具体类型切片", c: &Wcond{Column: "a", Op: OpIn, Arg: []string{"x", "y"}}, want: `"a" IN (?,?)`, wantArgs: []any{"x", "y"}},
		{name: "nin", c: &Wcond{Column: "a", Op: OpNotIn, Arg: []any{1, 2}}, want: `"a" NOT IN (?,?)`, wantArgs: []any{1, 2}},
		{name: "isnull", c: &Wcond{Column: "a", Op: OpIsNull}, want: `"a" IS NULL`},
		{name: "notnull", c: &Wcond{Column: "a", Op: OpNotNull}, want: `"a" IS NOT NULL`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := paged(Query{Table: "person", Columns: []string{"id"}, Where: group(CombAnd, c.c)})
			stmt, args, err := renderQuery(ANSI, q)
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			want := `SELECT "id" FROM "person" WHERE ` + c.want
			assertSQL(t, stmt, args, want, c.wantArgs)
		})
	}
}

// in/nin 的值必须是集合（切片）且非空 —— 值形态错误在渲染期报错。
func TestInOpValueShapeIsRenderError(t *testing.T) {
	cases := []struct {
		name string
		c    *Wcond
	}{
		{name: "in 给了标量", c: &Wcond{Column: "a", Op: OpIn, Arg: 1}},
		{name: "in 给了空集合", c: &Wcond{Column: "a", Op: OpIn, Arg: []any{}}},
		{name: "in 没给值", c: &Wcond{Column: "a", Op: OpIn}},
		{name: "nin 给了标量", c: &Wcond{Column: "a", Op: OpNotIn, Arg: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := paged(Query{Table: "person", Columns: []string{"id"}, Where: group(CombAnd, c.c)})
			_, _, err := renderQuery(ANSI, q)
			wantErr(t, err, "in/nin")
		})
	}
}

func TestUnknownOperatorIsRenderError(t *testing.T) {
	q := paged(Query{Table: "person", Columns: []string{"id"}, Where: group(CombAnd, cond("a", "nosuch", 1))})
	_, _, err := renderQuery(ANSI, q)
	wantErr(t, err, "未注册的算子")
}

// 节点种类由接口密封（Composite）："两个都给 / 两个都空"的联合形态在类型上
// 不可表达；剩下的非法形态只有 nil，渲染期报错。
func TestNilNodeIsRenderError(t *testing.T) {
	cases := []struct {
		name  string
		items []Node
		word  string
	}{
		{name: "叶子是 nil 指针", items: []Node{(*Wcond)(nil)}, word: "空指针"},
		{name: "组是 nil 指针", items: []Node{(*Wnode)(nil)}, word: "空指针"},
		{name: "节点是 nil 接口", items: []Node{nil}, word: "不能是 nil"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := paged(Query{Table: "person", Columns: []string{"id"}, Where: group(CombAnd, c.items...)})
			_, _, err := renderQuery(ANSI, q)
			wantErr(t, err, c.word)
		})
	}
}
