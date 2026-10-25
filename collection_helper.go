package slyorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"slyorm/dbwm"
	"slyorm/query"
	rows "slyorm/select" // 包名 rows：select 是 Go 关键字，不能作包名
)

// ---------------------------------------------------------------------------
// 构造校验
// ---------------------------------------------------------------------------

// checkTableName 校验表名：非空，且不含空白与引用字符。
func checkTableName(name string) error {
	if name == "" {
		return fmt.Errorf("slyorm: 表名为空")
	}
	if strings.ContainsAny(name, "\"` \t\r\n") {
		return fmt.Errorf("slyorm: 表名 %q 含非法字符（空白或引用字符）", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 条件构造（读规则 → Wnode；列校验对着 M 的列集合）
// ---------------------------------------------------------------------------

// where 把条件模型解析成条件树并校验列集合。
//
// q 为 nil（含类型化 nil 指针）时视为无条件，返回 nil —— 是"无条件"还是错误
// 由调用方按各自规则决定（Get 报错、Query 查全表，见 prd-slyorm R3/R4）。
func (c *Collection) where(ctx context.Context, q any) (*dbwm.Wnode, error) {
	if isNilModel(q) {
		return nil, nil
	}
	tpl, err := query.ParseWhere(q)
	if err != nil {
		return nil, fmt.Errorf("slyorm: 条件模型解析失败: %w", err)
	}
	for _, col := range tpl.Columns() {
		if !c.m.HasColumn(col) {
			return nil, fmt.Errorf("slyorm: 条件列 %q 不在表 %s 的列集合", col, c.table)
		}
	}
	w, err := tpl.Build(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("slyorm: 条件生成失败: %w", err)
	}
	return w, nil
}

// isNilModel 判定 q 是不是 nil（含类型化 nil 指针）。
func isNilModel(q any) bool {
	if q == nil {
		return true
	}
	rv := reflect.ValueOf(q)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// errNoCond 生成空条件时 Get / Update / Delete 的报错（带表名与操作，见 R13.1）。
func (c *Collection) errNoCond(op string) error {
	return fmt.Errorf("slyorm: 表 %s 的 %s 缺少过滤条件（所有条件字段都没有值或被 omit 跳过）", c.table, op)
}

// ---------------------------------------------------------------------------
// 投影列校验（读侧 R2.4 / 写侧 R7.1，错误带投影类型名，见 R13.1）
// ---------------------------------------------------------------------------

// projType 取投影 T 解指针后的类型（缓存键与错误信息用）。
func projType[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// checkRead 校验读侧投影 T 的列都在 M 的列集合里（R2.4 —— 投影是表列的
// 子集，越出表模型的列在这里拦下，不静默读模型外的列）；成功按 T 类型缓存。
func (c *Collection) checkRead[T any]() error {
	if err := checkProj[T](); err != nil {
		return err
	}
	t := projType[T]()
	if _, ok := c.readOK.Load(t); ok {
		return nil
	}
	p, err := rows.PlanOf(new(T))
	if err != nil {
		return fmt.Errorf("slyorm: 投影 %s 解析失败: %w", t, err)
	}
	for _, col := range p.Columns() {
		if !c.m.HasColumn(col) {
			return fmt.Errorf("slyorm: 投影 %s 的列 %q 不在表 %s 的列集合", t, col, c.table)
		}
	}
	c.readOK.Store(t, struct{}{})
	return nil
}

// checkProj 校验投影类型 T 不是指针（读侧与写侧共用；指针形态在 IR 构造 /
// 工厂建行之前拦下，不给误导性的下游错误，见评审 F4）。
func checkProj[T any]() error {
	if t := reflect.TypeFor[T](); t.Kind() == reflect.Pointer {
		return fmt.Errorf("slyorm: 投影类型 %s 不能是指针，请用值类型（bo.User 而不是 *bo.User）", t)
	}
	return nil
}

// checkWrite 校验写侧投影 T 的列集合（R7.1）；错误带投影类型名。
func (c *Collection) checkWrite[T any](cols []string) error {
	t := projType[T]()
	for _, col := range cols {
		if !c.m.HasColumn(col) {
			return fmt.Errorf("slyorm: 投影 %s 的列 %q 不在表 %s 的列集合", t, col, c.table)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 查询实现
// ---------------------------------------------------------------------------

// get 单行查询的实现：列校验 → 条件构造 → 取行；条件为空报错。
func (c *Collection) get[T any](ctx context.Context, q any) (*T, error) {
	if err := c.checkRead[T](); err != nil {
		return nil, err
	}
	w, err := c.where(ctx, q)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, c.errNoCond("Get")
	}
	return rows.QueryOne[T](ctx, c.wm, &dbwm.Query{Table: c.table, Where: w})
}

// query 多行查询的实现：列校验 → 条件构造 → 取行；条件为空 = 查全表。
func (c *Collection) query[T any](ctx context.Context, q any) ([]*T, error) {
	if err := c.checkRead[T](); err != nil {
		return nil, err
	}
	w, err := c.where(ctx, q)
	if err != nil {
		return nil, err
	}
	return rows.QueryAll[T](ctx, c.wm, &dbwm.Query{Table: c.table, Where: w})
}

// ---------------------------------------------------------------------------
// 写入辅助
// ---------------------------------------------------------------------------

// dropPK 从 SET 的列与值里剔除 M 的主键列（主键集合只认 M —— 投影自带的
// pk 标记不参与剔除判定，否则用投影就能绕过剔除，见 prd-slyorm R8.3）。
//
// 产出**新切片**：不就地覆写入参，避免调用方继续持有原切片时读到被改写的内容。
func (c *Collection) dropPK(cols []string, vals []any) ([]string, []any) {
	pks := c.m.PKNames()
	if len(pks) == 0 {
		return cols, vals
	}
	drop := make(map[string]bool, len(pks))
	for _, p := range pks {
		drop[p] = true
	}
	outC := make([]string, 0, len(cols))
	outV := make([]any, 0, len(vals))
	for i, col := range cols {
		if drop[col] {
			continue
		}
		outC = append(outC, col)
		outV = append(outV, vals[i])
	}
	return outC, outV
}
