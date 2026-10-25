// Package rows 是行模型读取层：投影计划、RowFactory 与扫描目标构造。
//
// 需求与规则见 docs/prd-slyorm.md R2 / R6。目录叫 select/、包名叫 rows ——
// select 是 Go 关键字，不能作包名（与 query 包的先例同款，见 prd-select §8）。
//
// 投影类型 T 是表列任意子集的字段组合（列名推导走 model 包，唯一入口），
// **必须是非指针结构体**（写成 *T 会在入口报错，不给误导性的下游错误）。
// 扫描目标自带字段地址：NULL 兜底 / 时间兼容 / JSON 反序列化三类包装在
// Scan 时直接写业务字段，不依赖扫后回调（dbwm 的 RowFactory 没有这个钩子）。
//
// 本包产出并执行 dbwm 的查询：Query 的 Columns 由计划填入（列序 = 扫描目标序），
// 门面（slyorm.Collection）只组 Where / Sorts / 分页。
//
// 文件分工：
//
//	select.go 本文件 —— 对外 API（Plan 与三个执行入口）
//	plan.go    行工厂与计划壳（模型缓存唯一，见 plan.go 说明）
//	scan.go    扫描目标构造（指针 / 时间 / JSON / NULL 兜底四类）
package rows

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"slyorm/dbwm"
	"slyorm/model"
)

// Plan 是一个投影类型的读取计划：静态列集合 + 扫描目标构造方式。
type Plan struct {
	m *model.Model
}

// Columns 返回投影列清单（声明序 = 扫描目标序）。
func (p *Plan) Columns() []string { return p.m.Names() }

// HasColumn 报告列是否在投影里。
func (p *Plan) HasColumn(name string) bool { return p.m.HasColumn(name) }

// PlanOf 解析一个投影类型（值或指针均可），计划包着 model 包的缓存元信息。
//
// T 未声明任何 dbx 字段 → 报错（避免拼出空列清单的 SELECT，见 prd-slyorm R2.5）。
func PlanOf(v any) (*Plan, error) {
	if v == nil {
		return nil, errors.New("rows: 投影模型不能为 nil")
	}
	m, err := model.Parse(v)
	if err != nil {
		return nil, err
	}
	return &Plan{m: m}, nil
}

// ---------------------------------------------------------------------------
// 执行入口（泛型在门面与这一层；dbwm 保持非泛型）
// ---------------------------------------------------------------------------

// checkT 校验投影类型 T 是非指针结构体。
//
// 指针型投影（*bo.User）在工厂里建不出行对象，最终会以 dbwm 的
// "列数不符"暴露 —— 在入口拦下并指明写法（prd-slyorm 评审 F4）。
func checkT[T any]() error {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		return fmt.Errorf("rows: 投影类型 %s 不能是指针，请用值类型（bo.User 而不是 *bo.User）", t)
	}
	return nil
}

// planOfT 取 T 的读取计划。
func planOfT[T any]() (*Plan, error) {
	if err := checkT[T](); err != nil {
		return nil, err
	}
	return PlanOf(new(T))
}

// QueryAll 全量查询：返回全部命中行；没有命中返回空切片（len 0，非 nil，
// 不是错误）。q.Columns 会被计划覆盖（列序必须与扫描地址对齐）。
func QueryAll[T any](ctx context.Context, db *dbwm.DB, q *dbwm.Query) ([]*T, error) {
	p, err := planOfT[T]()
	if err != nil {
		return nil, err
	}
	c := newCollector[T](p)
	q.Columns = p.Columns()
	if err := db.Qryall(ctx, q, c); err != nil {
		return nil, err
	}
	return c.rows, nil
}

// QueryOne 至多一行：没有命中返回 (nil, nil)（转译 dbwm.ErrNoRows，见 prd-slyorm R3.2）；
// 多于一行取第一行（语句带 LIMIT 1，语义同 dbwm.Qryone）。
func QueryOne[T any](ctx context.Context, db *dbwm.DB, q *dbwm.Query) (*T, error) {
	p, err := planOfT[T]()
	if err != nil {
		return nil, err
	}
	c := newCollector[T](p)
	q.Columns = p.Columns()
	if err := db.Qryone(ctx, q, c); err != nil {
		if errors.Is(err, dbwm.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(c.rows) == 0 {
		return nil, nil
	}
	return c.rows[0], nil
}

// QueryPaged 分页查询：返回总行数与本页行（总数由 dbwm 另发一条 COUNT）。
func QueryPaged[T any](ctx context.Context, db *dbwm.DB, pq *dbwm.PagedQuery) (int64, []*T, error) {
	p, err := planOfT[T]()
	if err != nil {
		return 0, nil, err
	}
	c := newCollector[T](p)
	pq.Columns = p.Columns()
	total, err := db.Qrypgs(ctx, pq, c)
	if err != nil {
		return 0, nil, err
	}
	return total, c.rows, nil
}
