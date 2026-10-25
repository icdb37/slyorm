package migration

import (
	"context"
	"errors"
	"fmt"

	"slyorm/dbwm"
)

// ---------------------------------------------------------------------------
// 执行记录表（sys_migration）
// ---------------------------------------------------------------------------

// TableMigration 执行记录表名：记录每个脚本的执行时间、是否成功与失败原因。
const TableMigration = "sys_migration"

// 记录表的列名常量（R3.6 单一来源）：建表 DDL 与留痕 IR 共用 —— 两份手工
// 副本漂移只会在运行时炸成 no such column。
const (
	colVersion  = "version"
	colSequence = "sequence"
	colName     = "name"
	colFile     = "file"
	colExecuted = "executed_at"
	colSuccess  = "success"
	colError    = "error"
)

// ddlMigration 建表语句：可移植 DDL（不带方言引用符，标识符全小写不加引号，
// 均为三库非保留字），由列名常量拼出；IF NOT EXISTS 幂等，对存量表是空转
// （R3.1 / R3.7）。
const ddlMigration = "CREATE TABLE IF NOT EXISTS " + TableMigration + " (\n" +
	"  " + colVersion + "     INTEGER       NOT NULL,\n" +
	"  " + colSequence + "    INTEGER       NOT NULL,\n" +
	"  " + colName + "       VARCHAR(200)  NOT NULL,\n" +
	"  " + colFile + "       VARCHAR(500)  NOT NULL,\n" +
	"  " + colExecuted + "   TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,\n" +
	"  " + colSuccess + "    INTEGER       NOT NULL,\n" +
	"  " + colError + "      VARCHAR(1000) NOT NULL\n)"

// recordColumns 留痕列：不含 executed_at（交给数据库 DEFAULT CURRENT_TIMESTAMP）；
// 不点名 id —— 存量表（mgx 形态，id 自增主键）由库自填、新表无此列，同一份
// 代码跑在两种结构上（R3.7）。
var recordColumns = []string{colVersion, colSequence, colName, colFile, colSuccess, colError}

// maxErrorLen 入库失败原因的最大字符数（R7.4）。
const maxErrorLen = 1000

// ensureTable 建执行记录表（IF NOT EXISTS 幂等；对 mgx 形态的存量表是空转）。
func ensureTable(ctx context.Context, db *dbwm.DB) error {
	if err := db.ExecScript(ctx, ddlMigration); err != nil {
		return fmt.Errorf("migration: 建执行记录表 %s 失败: %w", TableMigration, err)
	}
	return nil
}

// record 写一条执行记录（R3.3）：走 dbwm 的 Insert IR，占位符与标识符引用
// 随方言渲染；errText 是失败原因（成功传空串），按 R7.4 截断入库。
func record(ctx context.Context, db *dbwm.DB, sc *script, success bool, errText string) error {
	_, err := db.Insert(ctx, &dbwm.Insert{
		Table:   TableMigration,
		Columns: recordColumns,
		Rows: [][]any{{sc.version, sc.sequence, sc.name, sc.file,
			boolToInt(success), truncateRunes(errText, maxErrorLen)}},
	})
	if err != nil {
		return fmt.Errorf("migration: 写执行记录失败（%s）: %w", sc.file, err)
	}
	return nil
}

// watermark 取最近一次成功执行的（版本号, 序号）；无成功记录时为零值水位
// （ErrNoRows 转零值，不是错误，R4.1）。失败记录不计入 —— 失败的脚本下次
// 要重试（R4.2）。
func watermark(ctx context.Context, db *dbwm.DB) (int, int, error) {
	var version, sequence int
	f := dbwm.RowFactoryFunc(func() []any { return []any{&version, &sequence} })
	err := db.Qryone(ctx, &dbwm.Query{
		Table:   TableMigration,
		Columns: []string{colVersion, colSequence},
		Where: &dbwm.Wnode{Items: []dbwm.Node{
			&dbwm.Wcond{Column: colSuccess, Op: dbwm.OpEq, Arg: 1},
		}},
		Sorts: []string{"-" + colVersion, "-" + colSequence},
	}, f)
	if errors.Is(err, dbwm.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("migration: 读迁移水位失败: %w", err)
	}
	return version, sequence, nil
}

// boolToInt 布尔落库为 1/0。
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// truncateRunes 按字符（非字节）截断（R7.4）：UTF-8 按字节切会截坏多字节
// 字符，且 PG / MySQL 的 VARCHAR(1000) 本就按字符计。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
