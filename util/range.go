package util

import "reflect"

// ---------------------------------------------------------------------------
// 区间
// ---------------------------------------------------------------------------

// Interval 是区间类型的统一接口 —— 查询侧据此识别「区间字段」。
//
// 语义统一为**左闭右开**：下界含、上界不含。
type Interval interface {
	// HasMin 报告下界是否给了。
	HasMin() bool
	// HasMax 报告上界是否给了。
	HasMax() bool
	// MinValue 下界的值（下界没给时返回类型零值）。
	MinValue() any
	// MaxValue 上界的值（上界没给时返回类型零值）。
	MaxValue() any
}

// Range[T] 是左闭右开区间 [Min, Max)。
//
// 边界由**类型零值**决定：某一端是零值就当"没给"这一端；
// 两端都是零值（或指针为 nil）时整个区间不参与条件。
//
// 零值判定沿用写规则那一套（含 time.Time 用 IsZero()）。
//
// 它在写规则里是**单列类型**（整体存成一列，不按 inline 展开），
// 在读规则里是一个**组**（整体用括号包裹，放进 or 组不会被拆散）。
type Range[T any] struct {
	// Min 下界（含）。
	Min T
	// Max 上界（不含）。
	Max T
}

// HasMin 报告下界是否给了（Min 非零值）。
func (r Range[T]) HasMin() bool { return !IsZero(reflect.ValueOf(r.Min)) }

// HasMax 报告上界是否给了（Max 非零值）。
func (r Range[T]) HasMax() bool { return !IsZero(reflect.ValueOf(r.Max)) }

// MinValue 返回下界的值。
func (r Range[T]) MinValue() any { return r.Min }

// MaxValue 返回上界的值。
func (r Range[T]) MaxValue() any { return r.Max }
