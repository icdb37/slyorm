package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"slyorm/dbwm"

	_ "modernc.org/sqlite"
)

var tctx = context.Background()

// newTestDB 开一个内存库（单连接）注入 dbwm；返回句柄与底层 *sql.DB，
// 后者只用于测试断言。
func newTestDB(t *testing.T) (*dbwm.DB, *sql.DB) {
	t.Helper()
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	base.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = base.Close() })
	return dbwm.NewSQL(base), base
}

// countT 统计内容表 t 的行数（t 由脚本自建）。
func countT(t *testing.T, base *sql.DB) int {
	t.Helper()
	var n int
	if err := base.QueryRowContext(tctx, "SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countRecords 统计执行记录条数；success 为 nil 时不限成败。
func countRecords(t *testing.T, base *sql.DB, success *int) int {
	t.Helper()
	q := "SELECT COUNT(*) FROM " + TableMigration
	var args []any
	if success != nil {
		q += " WHERE " + colSuccess + " = ?"
		args = append(args, *success)
	}
	var n int
	if err := base.QueryRowContext(tctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// fetchRecord 取某脚本最近一条执行记录的（name, file, error）。
func fetchRecord(t *testing.T, base *sql.DB, version, sequence int) (string, string, string) {
	t.Helper()
	var name, file, errText string
	if err := base.QueryRowContext(tctx,
		"SELECT "+colName+", "+colFile+", "+colError+" FROM "+TableMigration+
			" WHERE "+colVersion+" = ? AND "+colSequence+" = ? ORDER BY rowid DESC LIMIT 1",
		version, sequence).Scan(&name, &file, &errText); err != nil {
		t.Fatal(err)
	}
	return name, file, errText
}

// tableExists 报告表是否存在。
func tableExists(t *testing.T, base *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := base.QueryRowContext(tctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// ---------------------------------------------------------------------------
// R1 入口与统计
// ---------------------------------------------------------------------------

func TestRunExecutesAllAndRecords(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_000_create.sql": "CREATE TABLE t (v TEXT NOT NULL);",
		"v1.0/1000_001_seed.sql":   "INSERT INTO t (v) VALUES ('a');",
		"v1.1/1100_001_more.sql":   "INSERT INTO t (v) VALUES ('b');",
		"v1.0/readme.txt":          "忽略非 sql 文件",
	})
	st, err := Run(tctx, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Scripts != 3 || st.Applied != 3 {
		t.Fatalf("Stats = %d/%d，期望 3/3", st.Scripts, st.Applied)
	}
	if n := countT(t, base); n != 2 {
		t.Fatalf("内容表 %d 行，期望 2 行", n)
	}
	if n := countRecords(t, base, nil); n != 3 {
		t.Fatalf("执行记录 %d 条，期望 3 条", n)
	}
	// R3.5 name / file 取值口径：名称段与 FS 内相对路径。
	name, file, errText := fetchRecord(t, base, 1000, 1)
	if name != "seed" || file != "v1.0/1000_001_seed.sql" || errText != "" {
		t.Fatalf("记录 = %s/%s/%q，期望 seed / v1.0/1000_001_seed.sql / \"\"", name, file, errText)
	}
}

func TestRunEmptyDirZeroSideEffect(t *testing.T) {
	db, base := newTestDB(t)
	st, err := Run(tctx, db, mapfs(map[string]string{"readme.txt": "没有脚本"}))
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stats{}) {
		t.Fatalf("Stats = %+v，期望零值", st)
	}
	if tableExists(t, base, TableMigration) {
		t.Fatal("空目录不该建 sys_migration 表（R1.4 零副作用）")
	}
}

func TestRunNilArgs(t *testing.T) {
	db, _ := newTestDB(t)
	if _, err := Run(tctx, db, nil); err == nil || !strings.Contains(err.Error(), "fs.FS") {
		t.Fatalf("nil FS 该报错，得 %v", err)
	}
	if _, err := Run(tctx, nil, mapfs(nil)); err == nil || !strings.Contains(err.Error(), "句柄") {
		t.Fatalf("nil db 该报错，得 %v", err)
	}
}

// ---------------------------------------------------------------------------
// R2 装载失败零副作用
// ---------------------------------------------------------------------------

func TestRunLoadFailureZeroSideEffect(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_ok.sql": "CREATE TABLE t (v TEXT);",
		"v1.0/bad_name.sql":    "INSERT INTO t (v) VALUES ('a');",
		"v1.0/1000_007_dup.sql": "SELECT 1;",
		"v1.0/1000_7_dup2.sql":  "SELECT 1;",
	})
	if _, err := Run(tctx, db, fsys); err == nil {
		t.Fatal("装载失败该报错")
	}
	if tableExists(t, base, TableMigration) {
		t.Fatal("装载失败不该建表（R2.5 零副作用）")
	}
}

// ---------------------------------------------------------------------------
// R3 记录表：列名一致性与存量表兼容
// ---------------------------------------------------------------------------

func TestDDLContainsAllRecordColumns(t *testing.T) {
	// R3.6 列名单一来源：留痕 IR 的每列都要出现在建表 DDL 里。
	for _, c := range recordColumns {
		if !strings.Contains(ddlMigration, c) {
			t.Fatalf("留痕列 %s 不在建表 DDL 里（两份列名漂移）", c)
		}
	}
}

func TestRunOnLegacyTable(t *testing.T) {
	db, base := newTestDB(t)
	// mgx 形态的存量表：id 自增主键 + error TEXT（R3.7 零改动兼容）。
	const legacyDDL = "CREATE TABLE sys_migration (\n" +
		"  id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,\n" +
		"  version INTEGER NOT NULL,\n" +
		"  sequence INTEGER NOT NULL,\n" +
		"  name TEXT NOT NULL,\n" +
		"  file TEXT NOT NULL,\n" +
		"  executed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,\n" +
		"  success INTEGER NOT NULL DEFAULT 0,\n" +
		"  error TEXT NOT NULL DEFAULT ''\n)"
	if _, err := base.ExecContext(tctx, legacyDDL); err != nil {
		t.Fatal(err)
	}
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_a.sql": "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
	})
	if _, err := Run(tctx, db, fsys); err != nil {
		t.Fatalf("存量表上 Run 失败: %v", err)
	}
	// id 由库自填（INSERT 不点名 id），留痕落在 error 列。
	var id int
	var errText string
	if err := base.QueryRowContext(tctx,
		"SELECT id, error FROM sys_migration WHERE success = 1").Scan(&id, &errText); err != nil {
		t.Fatal(err)
	}
	if id == 0 || errText != "" {
		t.Fatalf("存量表留痕 = id %d / error %q，期望 id 自填、error 空串", id, errText)
	}
	// 二次 Run：水位在存量表上照常生效（全部跳过）。
	st, err := Run(tctx, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied != 0 {
		t.Fatalf("二次 Run 执行了 %d 个脚本，期望 0", st.Applied)
	}
}

// ---------------------------------------------------------------------------
// R4 水位与跳过
// ---------------------------------------------------------------------------

func TestRunWatermarkBoundaries(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_first.sql": "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
	})
	if _, err := Run(tctx, db, fsys); err != nil {
		t.Fatal(err)
	}
	// 水位 (1000, 1)：低号跳过、同版本高号执行、新版本号执行。
	fsys["v1.0/1000_000_earlier.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO t (v) VALUES ('earlier');")}
	fsys["v1.0/1000_002_next.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO t (v) VALUES ('next');")}
	fsys["v1.1/1100_001_newver.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO t (v) VALUES ('newver');")}
	st, err := Run(tctx, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Applied != 2 {
		t.Fatalf("执行了 %d 个脚本，期望 2（earlier 被跳过）", st.Applied)
	}
	if n := countT(t, base); n != 3 {
		t.Fatalf("内容表 %d 行，期望 3 行（earlier 不落库）", n)
	}
	if n := countRecords(t, base, nil); n != 3 {
		t.Fatalf("执行记录 %d 条，期望 3 条（跳过不留痕）", n)
	}
}

func TestRunSecondRunAllSkipped(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_a.sql": "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
	})
	if _, err := Run(tctx, db, fsys); err != nil {
		t.Fatal(err)
	}
	st, err := Run(tctx, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if st.Scripts != 1 || st.Applied != 0 {
		t.Fatalf("二次 Stats = %d/%d，期望 1/0", st.Scripts, st.Applied)
	}
	if n := countRecords(t, base, nil); n != 1 {
		t.Fatalf("重复执行不应重复留痕，得 %d 条", n)
	}
}

// ---------------------------------------------------------------------------
// R5 执行事务与留痕
// ---------------------------------------------------------------------------

func TestRunFailureRollsBackAndRecords(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_ok.sql":    "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
		"v1.0/1000_002_bad.sql":   "INSERT INTO no_such_table (v) VALUES ('x');",
		"v1.0/1000_003_after.sql": "INSERT INTO t (v) VALUES ('b');",
	})
	st, err := Run(tctx, db, fsys)
	if err == nil {
		t.Fatal("脚本失败该返回错误")
	}
	if !strings.Contains(err.Error(), "v1.0/1000_002_bad.sql") {
		t.Fatalf("错误 = %v，该带脚本文件路径", err)
	}
	// R1.5 出错时 Stats 带失败前已成功数。
	if st.Scripts != 3 || st.Applied != 1 {
		t.Fatalf("Stats = %d/%d，期望 3/1", st.Scripts, st.Applied)
	}
	// 失败脚本回滚、失败即停：t 只有 ok 一行，其后脚本未执行。
	if n := countT(t, base); n != 1 {
		t.Fatalf("内容表 %d 行，期望 1 行", n)
	}
	ok, fail := 1, 0
	if n := countRecords(t, base, &ok); n != 1 {
		t.Fatalf("成功记录 %d 条，期望 1", n)
	}
	if n := countRecords(t, base, &fail); n != 1 {
		t.Fatalf("失败记录 %d 条，期望 1", n)
	}
	_, _, errText := fetchRecord(t, base, 1000, 2)
	if errText == "" {
		t.Fatal("失败记录该写失败原因")
	}
	// R4.2 失败不抬水位：修好脚本后重跑成功，重试留新行（一败一成两行）。
	fsys["v1.0/1000_002_bad.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO t (v) VALUES ('fixed');")}
	if _, err := Run(tctx, db, fsys); err != nil {
		t.Fatalf("重跑失败: %v", err)
	}
	if n := countT(t, base); n != 3 {
		t.Fatalf("重跑后内容表 %d 行，期望 3 行", n)
	}
	if n := countRecords(t, base, &ok); n != 3 {
		t.Fatalf("重跑后成功记录 %d 条，期望 3", n)
	}
}

func TestRunBlankScriptSucceeds(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_create.sql": "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
		"v1.0/1000_002_blank.sql":  "   \n\t  \n",
		"v1.0/1000_003_after.sql":  "INSERT INTO t (v) VALUES ('b');",
	})
	st, err := Run(tctx, db, fsys)
	if err != nil {
		t.Fatalf("空白脚本该按成功处理: %v", err)
	}
	if st.Applied != 3 {
		t.Fatalf("Applied = %d，期望 3（空白脚本记执行）", st.Applied)
	}
	// 空白脚本照常抬水位：其后的脚本执行了（t 有 2 行）。
	if n := countT(t, base); n != 2 {
		t.Fatalf("内容表 %d 行，期望 2 行", n)
	}
	ok := 1
	if n := countRecords(t, base, &ok); n != 3 {
		t.Fatalf("成功记录 %d 条，期望 3", n)
	}
}

func TestRunInsideOuterTxRollsBackAll(t *testing.T) {
	db, base := newTestDB(t)
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_ok.sql":  "CREATE TABLE t (v TEXT); INSERT INTO t (v) VALUES ('a');",
		"v1.0/1000_002_bad.sql": "INSERT INTO no_such_table (v) VALUES ('x');",
	})
	// R5.5 外层事务内 Run：全部脚本与留痕共用外层事务，失败随外层回滚
	// —— 脚本正文效果与 sys_migration 都不落库（SQLite 的 DDL 也在事务里，
	// 回滚后两张表一并消失）。
	err := db.Tx(tctx, func(ctx context.Context) error {
		_, err := Run(ctx, db, fsys)
		return err
	})
	if err == nil {
		t.Fatal("Run 失败该冒泡到外层")
	}
	if tableExists(t, base, TableMigration) || tableExists(t, base, "t") {
		t.Fatal("外层回滚后，留痕表与脚本建的表都该消失")
	}
}

// ---------------------------------------------------------------------------
// R7 留痕文本截断
// ---------------------------------------------------------------------------

func TestTruncateRunes(t *testing.T) {
	long := strings.Repeat("错", 1200) // 1200 个多字节字符
	got := truncateRunes(long, maxErrorLen)
	if n := len([]rune(got)); n != maxErrorLen {
		t.Fatalf("截断后 %d 字符，期望 %d", n, maxErrorLen)
	}
	if !strings.HasSuffix(got, "错") {
		t.Fatal("按字符截断不该切坏多字节字符")
	}
	if got := truncateRunes("短文本", maxErrorLen); got != "短文本" {
		t.Fatalf("短文本不该被改动: %q", got)
	}
}

// ---------------------------------------------------------------------------
// R8 ctx 取消
// ---------------------------------------------------------------------------

func TestRunCtxCancelled(t *testing.T) {
	db, base := newTestDB(t)
	ctx, cancel := context.WithCancel(tctx)
	cancel()
	fsys := mapfs(map[string]string{
		"v1.0/1000_001_a.sql": "CREATE TABLE t (v TEXT);",
	})
	_, err := Run(ctx, db, fsys)
	if err == nil {
		t.Fatal("ctx 取消该报错")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v，期望错误链里有 context.Canceled", err)
	}
	// 取消发生在建表之前：零痕迹属预期（R8.2）。
	if tableExists(t, base, TableMigration) {
		t.Fatal("取消后不该留 sys_migration 表")
	}
}
