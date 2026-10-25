package rows

import (
	"reflect"

	"github.com/icdb37/slyorm/dbwm"
)

// ---------------------------------------------------------------------------
// 行工厂
//
// Plan（对外类型，定义在 select.go）是 *model.Model 的薄壳 —— 不设第二层
// 缓存：模型缓存（model 包、按类型）就是唯一的缓存层，PlanOf 每次按需
// 新建壳，代价是一次结构体分配。
// ---------------------------------------------------------------------------

// collector 是一次查询的行工厂：每行 new(T)、收集进结果列表，
// 并按计划的列序返回各列扫描地址。
//
// 工厂与收集切片**每次调用新建**（prd-slyorm R6.1）。
type collector[T any] struct {
	p    *Plan
	rows []*T
}

// newCollector 构造一次查询的行工厂；结果切片预分配为空切片
// （无命中时返回 len 0 的非 nil 切片，见 prd-slyorm R4.2）。
func newCollector[T any](p *Plan) *collector[T] {
	return &collector[T]{p: p, rows: make([]*T, 0, 8)}
}

// NewRow 实现 dbwm.RowFactory：创建行对象、收集、返回扫描地址。
//
// 投影类型 T 必须是非指针结构体 —— 指针形态在入口（QueryAll 等）已拦下，
// 这里不再防御。
func (c *collector[T]) NewRow() []any {
	v := new(T)
	c.rows = append(c.rows, v)
	elem := reflect.ValueOf(v).Elem()
	return c.p.scanAddrs(elem)
}

// 确认 collector 满足 dbwm 的工厂契约。
var _ dbwm.RowFactory = (*collector[int])(nil)
