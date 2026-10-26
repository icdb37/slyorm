package dbwm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// 查询
//
// 行数据的落点由 RowFactory 决定：NewRow 返回一行各列的扫描地址，
// dbwm 把地址交给驱动扫描 —— 行对象在实现侧创建并收集（见 RowFactory）。
// dbwm 不认识行类型，因此不收集行对象：行数据从工厂里取 ——
// slyorm 层的泛型工厂正是这样包装出 []*T 的。
// ---------------------------------------------------------------------------

// ErrNoRows 表示查询没有命中行；调用方用 errors.Is 判定。
var ErrNoRows = errors.New("dbwm: 没有行")

// RowFactory 提供一行各列的扫描地址。
//
// NewRow 每行调用一次：实现侧**创建行对象（如 *Person）、把它收集进
// 结果列表**，再返回该行各列的扫描地址（顺序与 Query.Columns +
// Query.Aggs 一致）—— dbwm 只把地址交给驱动扫描，扫描完成的行对象
// 已经落在实现侧的列表里，查询结束直接取用。
//
// 行类型是模型（dbx）的事，dbwm 不认识它：泛型工厂由 slyorm 层实现
// （NewRow 里 new(T)、append 进 []*T、返回字段地址），业务侧因此
// 直接拿到 []*T，不需要断言。
type RowFactory interface {
	NewRow() []any
}

// RowFactoryFunc 把"返回一行扫描地址的函数"适配成 RowFactory ——
// slyorm 层泛型包装的常用形态（闭包里 new(T)、收集、返回字段地址）。
type RowFactoryFunc func() []any

// NewRow 实现 RowFactory。
func (f RowFactoryFunc) NewRow() []any { return f() }

// Qryall 查询全部行。
//
// 不翻页、不发 COUNT —— 要翻页（连带总数）用 Qrypgs。没有命中行不是
// 错误 —— 返回 nil；行数据从工厂取（见 RowFactory）。
func (db *DB) Qryall(ctx context.Context, q *Query, f RowFactory) error {
	if q == nil {
		return fmt.Errorf("dbwm: 查询描述不能为空")
	}
	_, err := db.query(ctx, &PagedQuery{Query: *q}, f)
	return err
}

// Qryone 查询至多一行：没有行返回 ErrNoRows。
//
// 取一行不翻页 —— 分页描述是"第 1 页、每页 1 条"，语句只带 LIMIT 1，
// 与 Qryall 同一条执行路径（query）。多于一行时取第一行（不报错）。
func (db *DB) Qryone(ctx context.Context, q *Query, f RowFactory) error {
	if q == nil {
		return fmt.Errorf("dbwm: 查询描述不能为空")
	}
	n, err := db.query(ctx, &PagedQuery{Query: *q, Paging: Paging{Page: 1, Size: 1}}, f)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoRows
	}
	return nil
}

// Qrypgs 分页查询：返回总行数，本页行从工厂取。
//
// 总数由 dbwm 另发一条 COUNT 得到 —— 与 SELECT 不在同一快照，两者之间
// 数据可能变化；需要一致时把调用包进 db.Tx。
//
// 有分组时 total 是**分组数**，不是行数。
func (db *DB) Qrypgs(ctx context.Context, q *PagedQuery, f RowFactory) (int64, error) {
	total, err := db.count(ctx, q)
	if err != nil {
		return 0, err
	}
	if _, err := db.query(ctx, q, f); err != nil {
		return 0, err
	}
	return total, nil
}

// query 执行一条查询并扫描全部返回行：三个查询入口共用的执行路径 ——
// Qryall 不带分页、Qryone 带第 1 页每页 1 条、Qrypgs 带调用方给的分页。
//
// 返回扫到的行数（Qryone 判"没有行"用）。
func (db *DB) query(ctx context.Context, q *PagedQuery, f RowFactory) (int, error) {
	stmt, args, err := renderQuery(db.dialect, q)
	if err != nil {
		return 0, err
	}
	qr, err := db.querierOf(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := qr.QueryContext(ctx, stmt, args...)
	if err != nil {
		return 0, db.execFail("执行查询语句", stmt, err)
	}
	defer rows.Close()
	return db.scanRows(rows, f)
}

// count 查总行数：有分组时是分组数。
func (db *DB) count(ctx context.Context, q *PagedQuery) (int64, error) {
	stmt, args, err := renderCount(db.dialect, q)
	if err != nil {
		return 0, err
	}
	qr, err := db.querierOf(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := qr.QueryRowContext(ctx, stmt, args...).Scan(&total); err != nil {
		return 0, db.execFail("读取总行数", stmt, err)
	}
	return total, nil
}

// scanRows 扫描结果集：每行 NewRow 拿扫描地址，交给驱动直接写入。
//
// 扫描前校验列数：实际返回的列数必须等于 NewRow 给的地址个数 ——
// 错位的扫描会静默写错字段，宁可报错。
//
// 驱动侧的扫描失败按数据库错误分类（值放不进目标字段是"类型与值不匹配"，
// 由扫描目标自己挂上哨兵，见 prd-error R5）。
func (db *DB) scanRows(rows *sql.Rows, f RowFactory) (int, error) {
	cols, err := rows.Columns()
	if err != nil {
		return 0, db.fail("读取结果集列信息", err)
	}
	n := 0
	for rows.Next() {
		targets := f.NewRow()
		if len(targets) != len(cols) {
			return 0, fmt.Errorf("dbwm: 列数不符：结果集 %d 列，扫描目标 %d 个", len(cols), len(targets))
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, db.fail("扫描行", err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, db.fail("读取结果集", err)
	}
	return n, nil
}
