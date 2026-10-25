package dbwm

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R6 执行与错误语义
// ---------------------------------------------------------------------------

func TestInsertSingleRow(t *testing.T) {
	db := newTestDB(t)
	res, err := db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"name", "age"},
		Rows:    [][]any{{"张三", 30}},
	})
	if err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("影响行数 = %d，期望 1", res.RowsAffected)
	}
	if len(res.IDs) != 0 {
		t.Fatalf("没指定自增列时不该返回自增值，实际 %v", res.IDs)
	}
}

func TestInsertReturnsAutoIDs(t *testing.T) {
	db := newTestDB(t)
	res, err := db.Insert(context.Background(), &Insert{
		Table:        "person",
		Columns:      []string{"name", "age"},
		Rows:         [][]any{{"张三", 30}, {"李四", 31}},
		AutoPKColumn: "id",
	})
	if err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	if res.RowsAffected != 2 {
		t.Fatalf("影响行数 = %d，期望 2", res.RowsAffected)
	}
	if len(res.IDs) != 2 {
		t.Fatalf("自增值 = %v，期望 2 个", res.IDs)
	}
	if res.IDs[0] != 1 || res.IDs[1] != 2 {
		t.Fatalf("自增值 = %v，期望 [1 2]", res.IDs)
	}
}

func TestInsertChunksByMaxParams(t *testing.T) {
	conn := &fakeConn{affected: 1, lastID: 5}
	db := New(conn, WithDialect(&tinyDialect{}))
	rows := make([][]any, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, []any{i, "n"})
	}
	res, err := db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"age", "name"},
		Rows:    rows,
	})
	if err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	// 上限 3 个参数、每行 2 列 → 每条语句一行。
	if len(conn.stmts) != 5 {
		t.Fatalf("执行了 %d 条语句，期望 5 条: %v", len(conn.stmts), conn.stmts)
	}
	if conn.stmts[0] != `INSERT INTO "person"("age","name") VALUES (?,?)` {
		t.Fatalf("首条语句 = %s", conn.stmts[0])
	}
	if res.RowsAffected != 5 {
		t.Fatalf("影响行数 = %d，期望 5", res.RowsAffected)
	}
}

// TestInsertReturningPath 验证走 RETURNING 取回自增值的那条路（PostgreSQL 的做法）：
// 自增值按行取回，不依赖推算。
func TestInsertReturningPath(t *testing.T) {
	db := newTestDB(t)
	ret := New(db.conn, WithDialect(returningDialect{}))
	res, err := ret.Insert(context.Background(), &Insert{
		Table:        "person",
		Columns:      []string{"name"},
		Rows:         [][]any{{"张三"}, {"李四"}},
		AutoPKColumn: "id",
	})
	if err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	if res.RowsAffected != 2 {
		t.Fatalf("影响行数 = %d，期望 2", res.RowsAffected)
	}
	if len(res.IDs) != 2 || res.IDs[0] != 1 || res.IDs[1] != 2 {
		t.Fatalf("自增值 = %v，期望 [1 2]", res.IDs)
	}
}

func TestInsertRejectsInvalidInput(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		in      *Insert
		keyword string
	}{
		{
			name:    "列清单为空",
			in:      &Insert{Table: "person", Rows: [][]any{{1}}},
			keyword: "列清单",
		},
		{
			name:    "没有可插入的行",
			in:      &Insert{Table: "person", Columns: []string{"name"}},
			keyword: "没有可插入的行",
		},
		{
			name:    "行的值个数与列个数不一致",
			in:      &Insert{Table: "person", Columns: []string{"name", "age"}, Rows: [][]any{{"张三"}}},
			keyword: "不一致",
		},
		{
			name: "自增列出现在列清单里",
			in: &Insert{Table: "person", Columns: []string{"id", "name"},
				Rows: [][]any{{1, "张三"}}, AutoPKColumn: "id"},
			keyword: "互斥",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.Insert(ctx, c.in)
			wantErr(t, err, c.keyword)
		})
	}
}

func TestUpdateRequiresWhere(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Update(context.Background(), &Update{
		Table:   "person",
		Columns: []string{"age"},
		Values:  []any{31},
	})
	wantErr(t, err, "WHERE")
}

func TestDeleteRequiresWhere(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Delete(context.Background(), &Delete{Table: "person"})
	wantErr(t, err, "WHERE")
}

func TestUpdateAndDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	insertPeople(t, db, "张三", "李四")

	res, err := db.Update(ctx, &Update{
		Table:   "person",
		Columns: []string{"age"},
		Values:  []any{40},
		Where:   group(CombAnd, cond("name", OpEq, "张三")),
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("影响行数 = %d，期望 1", res.RowsAffected)
	}

	res, err = db.Delete(ctx, &Delete{
		Table: "person",
		Where: group(CombAnd, cond("name", OpEq, "李四")),
	})
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("影响行数 = %d，期望 1", res.RowsAffected)
	}

	rows, err := collectAll(ctx, db, &Query{Table: "person", Columns: []string{"name"}}, personTargets("name"))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("剩下 %d 行，期望 1 行", len(rows))
	}
}

func TestDriverErrorIsWrapped(t *testing.T) {
	db := newTestDB(t)
	// 主键重复 → 驱动报唯一性冲突，错误里要能看到原始驱动错误。
	_, err := db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "张三"}},
	})
	if err != nil {
		t.Fatalf("首次插入不该失败: %v", err)
	}
	_, err = db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "李四"}},
	})
	if err == nil {
		t.Fatalf("主键重复该报错")
	}
	if !strings.Contains(err.Error(), "dbwm: ") {
		t.Fatalf("错误信息 = %q，期望以 dbwm: 开头", err.Error())
	}
	// R6.1：错误信息要指出"哪条语句"。
	if !strings.Contains(err.Error(), `INSERT INTO "person"`) {
		t.Fatalf("错误信息 = %q，期望包含语句文本", err.Error())
	}
}
