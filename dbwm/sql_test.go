package dbwm

import (
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// assertSQL 断言 SQL 文本与绑定参数。
func assertSQL(t *testing.T, stmt string, args []any, wantSQL string, wantArgs []any) {
	t.Helper()
	if stmt != wantSQL {
		t.Fatalf("SQL =\n%s\n期望\n%s", stmt, wantSQL)
	}
	if len(args) != len(wantArgs) {
		t.Fatalf("参数 = %v，期望 %d 个: %v", args, len(wantArgs), wantArgs)
	}
	for i := range args {
		if args[i] != wantArgs[i] {
			t.Fatalf("第 %d 个参数 = %v，期望 %v", i, args[i], wantArgs[i])
		}
	}
}

// wantErr 断言出错，且错误信息里含有关键字（错误信息要能定位问题）。
func wantErr(t *testing.T, err error, keyword string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望报错，实际没有")
	}
	if !strings.Contains(err.Error(), keyword) {
		t.Fatalf("错误信息 = %q，期望包含 %q", err.Error(), keyword)
	}
}

// cond 造一条叶子条件。
func cond(column string, op Op, arg any) Node {
	return &Wcond{Column: column, Op: op, Arg: arg}
}

// group 造一个条件组。
func group(comb Comb, items ...Node) *Wnode {
	return &Wnode{Comb: comb, Items: items}
}

// paged 把查询描述包成带分页的查询（分页为零值 —— 不限行数）。
func paged(q Query) *PagedQuery {
	return &PagedQuery{Query: q}
}

// pagedWith 把查询描述包成带分页的查询。
func pagedWith(q Query, p Paging) *PagedQuery {
	return &PagedQuery{Query: q, Paging: p}
}

// ---------------------------------------------------------------------------
// R4 语句渲染
// ---------------------------------------------------------------------------

func TestSelectColumns(t *testing.T) {
	stmt, args, err := renderQuery(ANSI, paged(Query{Table: "person", Columns: []string{"id", "name"}}))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id","name" FROM "person"`, nil)
}

// 不支持 SELECT *：列清单为空渲染期直接报错。
func TestSelectRejectsEmptyColumns(t *testing.T) {
	_, _, err := renderQuery(ANSI, paged(Query{Table: "person"}))
	wantErr(t, err, "SELECT *")
}

func TestSelectWhere(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("name", OpEq, "张三"), cond("age", OpGt, 30)),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "id" FROM "person" WHERE "name" = ? AND "age" > ?`,
		[]any{"张三", 30})
}

func TestSelectSchemaTable(t *testing.T) {
	stmt, args, err := renderQuery(ANSI, paged(Query{Table: "public.person", Columns: []string{"id"}}))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "public"."person"`, nil)
}

func TestSelectGroupByAgg(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept"`, nil)
}

func TestSelectHavingRewritesAggAlias(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Having:  group(CombAnd, cond("cnt", OpGt, 10)),
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept" HAVING COUNT(*) > ?`,
		[]any{10})
}

func TestSelectAggregateFunctions(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs: []*Agg{
			{Func: AggCount, Alias: "cnt"},
			{Func: AggSum, Column: "age", Alias: "sum_age"},
			{Func: AggAvg, Column: "age", Alias: "avg_age"},
			{Func: AggMin, Column: "age", Alias: "min_age"},
			{Func: AggMax, Column: "age", Alias: "max_age"},
		},
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt", SUM("age") AS "sum_age", AVG("age") AS "avg_age", `+
			`MIN("age") AS "min_age", MAX("age") AS "max_age" FROM "person" GROUP BY "dept"`, nil)
}

func TestHavingRejectsUnknownColumn(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Having:  group(CombAnd, cond("name", OpEq, "张三")),
	})
	_, _, err := renderQuery(ANSI, q)
	wantErr(t, err, "只能引用分组列或聚合别名")
}

func TestSelectOrderBy(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Sorts:   []string{"-id", "+name", "age"},
	})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "id" FROM "person" ORDER BY "id" DESC, "name", "age"`, nil)
}

func TestSelectPaging(t *testing.T) {
	q := pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 3, Size: 10})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person" LIMIT 10 OFFSET 20`, nil)
}

func TestSelectNoPagingWhenSizeZero(t *testing.T) {
	q := pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 1, Size: 0})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person"`, nil)
}

// R7.2 取一行：分页描述是"第 1 页、每页 1 条"，语句只带 LIMIT 1、不带偏移。
func TestOneRowIsFirstPageOfSizeOne(t *testing.T) {
	stmt, args, err := renderQuery(ANSI,
		pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 1, Size: 1}))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT "id" FROM "person" LIMIT 1`, nil)
}

// 取一行遇到分组查询：LIMIT 1 落在 GROUP BY 之后。
func TestOneRowWithGroup(t *testing.T) {
	q := pagedWith(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
	}, Paging{Page: 1, Size: 1})
	stmt, args, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept" LIMIT 1`, nil)
}

func TestCountWithoutGroup(t *testing.T) {
	q := paged(Query{Table: "person", Columns: []string{"id"},
		Where: group(CombAnd, cond("age", OpGt, 30))})
	stmt, args, err := renderCount(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `SELECT COUNT(*) FROM "person" WHERE "age" > ?`, []any{30})
}

func TestCountWithGroupIsGroupCount(t *testing.T) {
	q := pagedWith(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Having:  group(CombAnd, cond("cnt", OpGt, 10)),
		Sorts:   []string{"-cnt"},
	}, Paging{Page: 1, Size: 20})
	stmt, args, err := renderCount(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	// 内层子查询带分组与 HAVING（聚合在内层可见），不带 ORDER BY 与分页。
	assertSQL(t, stmt, args,
		`SELECT COUNT(*) FROM (SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept" HAVING COUNT(*) > ?) "dbwm_sub"`,
		[]any{10})
}

func TestInsertSQL(t *testing.T) {
	stmt, args, err := renderInsert(ANSI, "person", []string{"name", "age"},
		[][]any{{"张三", 30}, {"李四", 31}})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`INSERT INTO "person"("name","age") VALUES (?,?),(?,?)`,
		[]any{"张三", 30, "李四", 31})
}

func TestUpdateSQL(t *testing.T) {
	up := &Update{
		Table:   "person",
		Columns: []string{"age"},
		Values:  []any{31},
		Where:   group(CombAnd, cond("id", OpEq, 7)),
	}
	stmt, args, err := renderUpdate(ANSI, up)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `UPDATE "person" SET "age"=? WHERE "id" = ?`, []any{31, 7})
}

func TestDeleteSQL(t *testing.T) {
	del := &Delete{Table: "person", Where: group(CombAnd, cond("id", OpEq, 7))}
	stmt, args, err := renderDelete(ANSI, del)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args, `DELETE FROM "person" WHERE "id" = ?`, []any{7})
}

// ---------------------------------------------------------------------------
// R4 渲染期校验
// ---------------------------------------------------------------------------

func TestRenderRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		q       *PagedQuery
		keyword string
	}{
		{
			name:    "表名为空",
			q:       paged(Query{Columns: []string{"id"}}),
			keyword: "表名",
		},
		{
			name:    "分组列不在列清单里",
			q:       paged(Query{Table: "person", Columns: []string{"id"}, GroupBy: []string{"dept"}}),
			keyword: "分组列",
		},
		{
			name:    "有聚合没有分组",
			q:       paged(Query{Table: "person", Columns: []string{"id"}, Aggs: []*Agg{{Func: AggCount, Alias: "cnt"}}}),
			keyword: "分组维度",
		},
		{
			name: "聚合别名与列名重名",
			q: paged(Query{Table: "person", Columns: []string{"id"}, GroupBy: []string{"id"},
				Aggs: []*Agg{{Func: AggCount, Alias: "id"}}}),
			keyword: "重名",
		},
		{
			name: "聚合别名重复",
			q: paged(Query{Table: "person", Columns: []string{"dept"}, GroupBy: []string{"dept"},
				Aggs: []*Agg{{Func: AggCount, Alias: "cnt"}, {Func: AggSum, Column: "age", Alias: "cnt"}}}),
			keyword: "重复",
		},
		{
			name: "非 count 的聚合没有列名",
			q: paged(Query{Table: "person", Columns: []string{"dept"}, GroupBy: []string{"dept"},
				Aggs: []*Agg{{Func: AggSum, Alias: "s"}}}),
			keyword: "必须指定列名",
		},
		{
			name: "聚合项是空指针",
			q: paged(Query{Table: "person", Columns: []string{"dept"}, GroupBy: []string{"dept"},
				Aggs: []*Agg{nil}}),
			keyword: "聚合项",
		},
		{
			name: "HAVING 没有分组维度",
			q: paged(Query{Table: "person", Columns: []string{"id"},
				Having: group(CombAnd, cond("id", OpEq, 1))}),
			keyword: "分组维度",
		},
		{
			name: "分组查询按非分组列排序",
			q: paged(Query{Table: "person", Columns: []string{"dept"}, GroupBy: []string{"dept"},
				Sorts: []string{"name"}}),
			keyword: "排序",
		},
		{
			name:    "页码为 0",
			q:       pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 0, Size: 10}),
			keyword: "页码",
		},
		{
			name:    "每页条数为 0 却给了页码",
			q:       pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 2, Size: 0}),
			keyword: "页码",
		},
		{
			name:    "每页条数为负",
			q:       pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 1, Size: -10}),
			keyword: "不能为负",
		},
		{
			name:    "不限行数但页码为负",
			q:       pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: -1, Size: 0}),
			keyword: "页码只能是 0 或 1",
		},
		{
			name:    "排序项只有方向前缀没有列名",
			q:       paged(Query{Table: "person", Columns: []string{"id"}, Sorts: []string{"-"}}),
			keyword: "没有列名",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := renderQuery(ANSI, c.q)
			wantErr(t, err, c.keyword)
		})
	}
}

// 两个渲染入口对 nil 查询描述统一报错（同一句），不 panic。
func TestRenderRejectsNilQuery(t *testing.T) {
	_, _, err := renderQuery(ANSI, nil)
	wantErr(t, err, "查询描述不能为空")
	_, _, err = renderCount(ANSI, nil)
	wantErr(t, err, "查询描述不能为空")
}

func TestUpdateRejectsEmptyWhere(t *testing.T) {
	up := &Update{Table: "person", Columns: []string{"age"}, Values: []any{31}}
	_, _, err := renderUpdate(ANSI, up)
	wantErr(t, err, "WHERE")
}

func TestDeleteRejectsEmptyWhere(t *testing.T) {
	_, _, err := renderDelete(ANSI, &Delete{Table: "person"})
	wantErr(t, err, "WHERE")
}

// ---------------------------------------------------------------------------
// R5 占位符与参数顺序
// ---------------------------------------------------------------------------

func TestPlaceholdersAreContinuousInStatement(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Where:   group(CombAnd, cond("age", OpGt, 30)),
		Having:  group(CombAnd, cond("cnt", OpGt, 10)),
	})
	stmt, args, err := renderQuery(PostgreSQL, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`SELECT "dept", COUNT(*) AS "cnt" FROM "person" WHERE "age" > $1 GROUP BY "dept" HAVING COUNT(*) > $2`,
		[]any{30, 10})
}

// 占位符编号跟着文本顺序走：UPDATE 是 SET 值 → WHERE 条件。
func TestUpdatePlaceholdersFollowSetThenWhere(t *testing.T) {
	up := &Update{
		Table:   "person",
		Columns: []string{"name", "age"},
		Values:  []any{"张三", 31},
		Where:   group(CombAnd, cond("id", OpEq, 7)),
	}
	stmt, args, err := renderUpdate(PostgreSQL, up)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, stmt, args,
		`UPDATE "person" SET "name"=$1,"age"=$2 WHERE "id" = $3`,
		[]any{"张三", 31, 7})
}

// 渲染器是单条语句私有的：同一个 Query 并发渲染互不影响（跑 -race 验证）。
func TestConcurrentRenderIsIndependent(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Where:   group(CombAnd, cond("age", OpGt, 30)),
		Having:  group(CombAnd, cond("cnt", OpGt, 10)),
	})
	const want = `SELECT "dept", COUNT(*) AS "cnt" FROM "person" WHERE "age" > $1 GROUP BY "dept" HAVING COUNT(*) > $2`
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stmt, args, err := renderQuery(PostgreSQL, q)
			if err != nil {
				t.Errorf("渲染失败: %v", err)
				return
			}
			if stmt != want {
				t.Errorf("SQL =\n%s\n期望\n%s", stmt, want)
			}
			if len(args) != 2 || args[0] != 30 || args[1] != 10 {
				t.Errorf("参数 = %v，期望 [30 10]", args)
			}
		}()
	}
	wg.Wait()
}

// 渲染不改写调用方的查询描述 —— 同一个描述渲染两次结果一致。
func TestRenderDoesNotMutateQuery(t *testing.T) {
	q := pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 2, Size: 10})
	first, _, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	second, _, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if first != second {
		t.Fatalf("两次渲染结果不同:\n%s\n%s", first, second)
	}
	if q.Paging != (Paging{Page: 2, Size: 10}) {
		t.Fatalf("分页参数被改写: %+v", q.Paging)
	}
}

func TestPlaceholderCountMismatchIsRenderError(t *testing.T) {
	if err := RegisterOperator("badcount", func(d Dialect, c *Wcond) (string, []any, error) {
		return QuoteIdent(d, c.Column) + " = ? AND ? = ?", []any{1}, nil
	}); err != nil {
		t.Fatalf("注册算子失败: %v", err)
	}
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("name", "badcount", "x")),
	})
	_, _, err := renderQuery(ANSI, q)
	wantErr(t, err, "占位符")
}

// ---------------------------------------------------------------------------
// R1 IR 跨方言复用
// ---------------------------------------------------------------------------

func TestSameQueryRendersPerDialect(t *testing.T) {
	q := paged(Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("name", OpEq, "张三")),
	})
	ansiSQL, ansiArgs, err := renderQuery(ANSI, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	pgSQL, pgArgs, err := renderQuery(PostgreSQL, q)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	assertSQL(t, ansiSQL, ansiArgs, `SELECT "id" FROM "person" WHERE "name" = ?`, []any{"张三"})
	assertSQL(t, pgSQL, pgArgs, `SELECT "id" FROM "person" WHERE "name" = $1`, []any{"张三"})
}

func TestPagingUnsupportedDialectIsRenderError(t *testing.T) {
	q := pagedWith(Query{Table: "person", Columns: []string{"id"}}, Paging{Page: 1, Size: 10})
	_, _, err := renderQuery(noPagingDialect{}, q)
	wantErr(t, err, "不支持分页")
}
