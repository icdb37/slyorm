package dbwm

import (
	"context"
	"fmt"
	"slices"
)

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

// Insert 执行插入。
//
// 多行一个语句（批量）；行数按方言的参数上限自动分块 —— 分块产生的多条
// 语句**不自动包事务**：要么由上层包 db.Tx，要么接受部分成功。
//
// AutoPKColumn 非空时取回数据库生成的自增值（Result.IDs，按行序对应 Rows）。
// 写回模型字段是上层的活 —— dbwm 不认识模型。
func (db *DB) Insert(ctx context.Context, in *Insert) (Result, error) {
	if err := validateInsert(in); err != nil {
		return Result{}, err
	}
	rowsPerChunk := db.dialect.MaxParams() / len(in.Columns)
	if rowsPerChunk < 1 {
		return Result{}, fmt.Errorf("dbwm: 列数 %d 超过一条语句的参数上限 %d",
			len(in.Columns), db.dialect.MaxParams())
	}
	var out Result
	for start := 0; start < len(in.Rows); start += rowsPerChunk {
		end := min(start+rowsPerChunk, len(in.Rows))
		res, err := db.execInsertChunk(ctx, in.Table, in.Columns, in.Rows[start:end], in.AutoPKColumn)
		out.RowsAffected += res.RowsAffected
		out.IDs = append(out.IDs, res.IDs...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// validateInsert 校验插入描述：列清单、行与自增列的互斥关系。
func validateInsert(in *Insert) error {
	if in == nil {
		return fmt.Errorf("dbwm: 插入描述不能为空")
	}
	if len(in.Columns) == 0 {
		return fmt.Errorf("dbwm: 插入的列清单不能为空")
	}
	if len(in.Rows) == 0 {
		return fmt.Errorf("dbwm: 没有可插入的行")
	}
	for i, row := range in.Rows {
		if len(row) != len(in.Columns) {
			return fmt.Errorf("dbwm: 第 %d 行的值个数（%d）与列个数（%d）不一致",
				i+1, len(row), len(in.Columns))
		}
	}
	if in.AutoPKColumn != "" {
		if slices.Contains(in.Columns, in.AutoPKColumn) {
			return fmt.Errorf(
				"dbwm: 自增列 %q 不能出现在列清单里（要么交给数据库生成并取回，要么写显式值，二者互斥）",
				in.AutoPKColumn)
		}
	}
	return nil
}

// execInsertChunk 执行一批行的 INSERT。
//
// 取自增值有两条路：
//   - 方言支持 RETURNING（PostgreSQL）：改成查询执行，按行拿真实值；
//   - 其余：ExecContext 后由方言从 LastInsertId 推算（见 BatchAutoIDs）。
func (db *DB) execInsertChunk(ctx context.Context, table string, columns []string, rows [][]any, autoPK string) (Result, error) {
	stmt, args, err := renderInsert(db.dialect, table, columns, rows)
	if err != nil {
		return Result{}, err
	}
	if autoPK != "" {
		if ret := db.dialect.Returning(autoPK); ret != "" {
			return db.execReturning(ctx, stmt+ret, args)
		}
	}
	ex, err := db.execerOf(ctx)
	if err != nil {
		return Result{}, err
	}
	res, err := ex.ExecContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, db.execFail("执行 INSERT 语句", stmt, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Result{}, db.execFail("取影响行数", stmt, err)
	}
	out := Result{RowsAffected: affected}
	if autoPK == "" {
		return out, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return out, db.execFail("取自增值", stmt, err)
	}
	ids, ok := db.dialect.BatchAutoIDs(id, len(rows))
	if !ok {
		return out, fmt.Errorf(
			"dbwm: %s 方言下批量插入的自增值无法可靠推算（LastInsertId 只给第一行）：请逐条插入，或给自增列显式赋值",
			db.dialect.Name())
	}
	out.IDs = ids
	return out, nil
}

// execReturning 执行带 RETURNING 子句的 INSERT，从结果集里取回自增值。
//
// 不依赖推算，每行拿到的都是数据库返回的真实值。
func (db *DB) execReturning(ctx context.Context, stmt string, args []any) (Result, error) {
	qr, err := db.querierOf(ctx)
	if err != nil {
		return Result{}, err
	}
	rows, err := qr.QueryContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, db.execFail("执行 INSERT 语句", stmt, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return Result{}, db.execFail("读取自增值", stmt, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return Result{}, db.execFail("执行 INSERT 语句", stmt, err)
	}
	return Result{RowsAffected: int64(len(ids)), IDs: ids}, nil
}

// Update 执行更新。
//
// WHERE 条件为空直接报错 —— 全表更新是误操作的主要来源。
func (db *DB) Update(ctx context.Context, up *Update) (Result, error) {
	if err := validateUpdate(up); err != nil {
		return Result{}, err
	}
	stmt, args, err := renderUpdate(db.dialect, up)
	if err != nil {
		return Result{}, err
	}
	ex, err := db.execerOf(ctx)
	if err != nil {
		return Result{}, err
	}
	res, err := ex.ExecContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, db.execFail("执行 UPDATE 语句", stmt, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Result{}, db.execFail("取影响行数", stmt, err)
	}
	return Result{RowsAffected: affected}, nil
}

// validateUpdate 校验更新描述：列清单与值要对齐。
func validateUpdate(up *Update) error {
	if up == nil {
		return fmt.Errorf("dbwm: 更新描述不能为空")
	}
	if len(up.Columns) == 0 {
		return fmt.Errorf("dbwm: 更新的列清单不能为空")
	}
	if len(up.Values) != len(up.Columns) {
		return fmt.Errorf("dbwm: 更新的值个数（%d）与列个数（%d）不一致",
			len(up.Values), len(up.Columns))
	}
	return nil
}

// Delete 执行删除。
//
// WHERE 条件为空直接报错 —— 全表删除是误操作的主要来源。
func (db *DB) Delete(ctx context.Context, del *Delete) (Result, error) {
	if del == nil {
		return Result{}, fmt.Errorf("dbwm: 删除描述不能为空")
	}
	stmt, args, err := renderDelete(db.dialect, del)
	if err != nil {
		return Result{}, err
	}
	ex, err := db.execerOf(ctx)
	if err != nil {
		return Result{}, err
	}
	res, err := ex.ExecContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, db.execFail("执行 DELETE 语句", stmt, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Result{}, db.execFail("取影响行数", stmt, err)
	}
	return Result{RowsAffected: affected}, nil
}
