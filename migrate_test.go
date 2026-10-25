package slyorm

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Migrate：门面转发（语义用例在 migration 包，这里验门面接线与业务形态）
// ---------------------------------------------------------------------------

// trow 是脚本建的表 t 的行模型（门面查询用）。
type trow struct {
	V string `dbx:"v"`
}

// tnoQuery 空条件（q 为 nil 时 Query 查全表，prd-slyorm R4.2）。
type tnoQuery struct{}

func TestMigrateForward(t *testing.T) {
	ctx := context.Background()
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fsys := fstest.MapFS{
		"v1.0/1000_001_init.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE t (v TEXT NOT NULL); INSERT INTO t (v) VALUES ('a'), ('b');")},
	}
	st, err := db.Migrate(ctx, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Scripts != 1 || st.Applied != 1 {
		t.Fatalf("Stats = %d/%d，期望 1/1", st.Scripts, st.Applied)
	}
	// 二次执行全部跳过（水位生效）。
	st, err = db.Migrate(ctx, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied != 0 {
		t.Fatalf("二次 Applied = %d，期望 0", st.Applied)
	}
	// 脚本效果经门面查询验证：Collection 能查到脚本写入的行。
	col, err := NewCollection[trow](db, "t")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := col.Query[tnoQuery, trow](ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("内容表 %d 行，期望 2 行", len(rows))
	}
}

func TestMigrateFailureBubbles(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fsys := fstest.MapFS{
		"v1.0/1000_001_bad.sql": &fstest.MapFile{Data: []byte(
			"INSERT INTO no_such_table (v) VALUES ('x');")},
	}
	if _, err := db.Migrate(context.Background(), fsys); err == nil ||
		!strings.Contains(err.Error(), "v1.0/1000_001_bad.sql") {
		t.Fatalf("错误 = %v，期望带脚本文件路径", err)
	}
}
