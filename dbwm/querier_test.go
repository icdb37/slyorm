package dbwm

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// R7 查询接口
// ---------------------------------------------------------------------------

func TestQryall(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四", "王五")

	rows, err := collectAll(ctx, db, &Query{
		Table:   "person",
		Columns: []string{"id", "name"},
		Sorts:   []string{"id"},
	}, personTargets("id", "name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("查出 %d 行，期望 3 行", len(rows))
	}
	if rows[0].Name != "张三" {
		t.Fatalf("首行 = %s，期望 张三", rows[0].Name)
	}
}

func TestQryallNoRowsIsNotError(t *testing.T) {
	db := newTestDB(t)
	rows, err := collectAll(context.Background(), db, &Query{
		Table:   "person",
		Columns: []string{"id"},
	}, personTargets("id"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("查出 %d 行，期望 0 行", len(rows))
	}
}

func TestQryone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四")

	rows, err := collectOne(ctx, db, &Query{
		Table:   "person",
		Columns: []string{"name"},
		Where:   group(CombAnd, cond("name", OpEq, "李四")),
	}, personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("查出 %d 行，期望 1 行", len(rows))
	}
	if rows[0].Name != "李四" {
		t.Fatalf("查出 %v，期望 李四", rows[0])
	}
}

func TestQryoneTakesFirstRowWhenMoreThanOne(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四")

	rows, err := collectOne(ctx, db, &Query{
		Table:   "person",
		Columns: []string{"name"},
		Sorts:   []string{"id"},
	}, personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("查出 %d 行，期望 1 行", len(rows))
	}
	if rows[0].Name != "张三" {
		t.Fatalf("查出 %v，期望 首行 张三", rows[0])
	}
}

func TestQryoneNotFound(t *testing.T) {
	db := newTestDB(t)
	_, err := collectOne(context.Background(), db, &Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("name", OpEq, "查无此人")),
	}, personTargets("id"))
	if !errors.Is(err, ErrNoRows) {
		t.Fatalf("错误 = %v，期望是 ErrNoRows", err)
	}
}

// 取一行对 nil 查询描述报错，不 panic。
func TestQryoneRejectsNilQuery(t *testing.T) {
	db := newTestDB(t)
	_, err := collectOne(context.Background(), db, nil, personTargets("name"))
	wantErr(t, err, "查询描述不能为空")
}

// 取一行不改写查询描述 —— 之后的全量查询仍返回全部行。
func TestQryoneDoesNotTruncateLaterQuery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四", "王五")

	q := Query{Table: "person", Columns: []string{"name"}, Sorts: []string{"id"}}
	one, err := collectOne(ctx, db, &q, personTargets("name"))
	if err != nil {
		t.Fatalf("取一行失败: %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("取一行查出 %d 行，期望 1 行", len(one))
	}
	all, err := collectAll(ctx, db, &q, personTargets("name"))
	if err != nil {
		t.Fatalf("全量查询失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全量查询查出 %d 行，期望 3 行", len(all))
	}
}

func TestQrypgs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四", "王五", "赵六", "钱七")

	q := pagedWith(Query{
		Table:   "person",
		Columns: []string{"name"},
		Sorts:   []string{"id"},
	}, Paging{Page: 2, Size: 2})
	rows, total, err := collectPgs(ctx, db, q, personTargets("name"))
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if total != 5 {
		t.Fatalf("总行数 = %d，期望 5", total)
	}
	if len(rows) != 2 {
		t.Fatalf("本页 %d 行，期望 2 行", len(rows))
	}
	if rows[0].Name != "王五" {
		t.Fatalf("本页首行 = %v，期望 王五", rows[0])
	}
}

func TestQrypgsTotalIsGroupCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, dept := range []string{"dev", "dev", "ops"} {
		if _, err := db.Insert(ctx, &Insert{
			Table:   "person",
			Columns: []string{"name", "dept"},
			Rows:    [][]any{{"x", dept}},
		}); err != nil {
			t.Fatalf("插入失败: %v", err)
		}
	}
	rows, total, err := collectPgs(ctx, db, pagedWith(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
	}, Paging{Page: 1, Size: 10}), personTargets("dept", "cnt"))
	if err != nil {
		t.Fatalf("分组查询失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("总行数 = %d，期望分组数 2", total)
	}
	if len(rows) != 2 {
		t.Fatalf("查出 %d 行，期望 2 个分组", len(rows))
	}
}

func TestQrypgsHavingFiltersGroups(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, dept := range []string{"dev", "dev", "dev", "ops"} {
		if _, err := db.Insert(ctx, &Insert{
			Table:   "person",
			Columns: []string{"name", "dept"},
			Rows:    [][]any{{"x", dept}},
		}); err != nil {
			t.Fatalf("插入失败: %v", err)
		}
	}
	rows, total, err := collectPgs(ctx, db, pagedWith(Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*Agg{{Func: AggCount, Alias: "cnt"}},
		Having:  group(CombAnd, cond("cnt", OpGt, 2)),
	}, Paging{Page: 1, Size: 10}), personTargets("dept", "cnt"))
	if err != nil {
		t.Fatalf("分组查询失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("总行数 = %d，期望 1（只有 dev 组的人数超过 2）", total)
	}
	if len(rows) != 1 {
		t.Fatalf("查出 %d 行，期望 1 行", len(rows))
	}
	if rows[0].Dept != "dev" {
		t.Fatalf("分组 = %v，期望 dev", rows[0])
	}
}

// ---------------------------------------------------------------------------
// R8 行接收
// ---------------------------------------------------------------------------

func TestScanRejectsColumnCountMismatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三")

	// 列清单两列，扫描地址只给一个 → 报错，不把错位的扫描交给驱动。
	_, err := collectAll(ctx, db, &Query{
		Table:   "person",
		Columns: []string{"id", "name"},
	}, personTargets("name"))
	wantErr(t, err, "列数不符")
}
