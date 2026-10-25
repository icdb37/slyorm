package slyorm

import "github.com/icdb37/slyorm/util"

// ---------------------------------------------------------------------------
// 通用数据库类型
//
// 业务侧 cco 里的通用数据库类型收进门面统一组织（见 prd-slyorm R11）；
// 纯数据 + dbx 标签按当前规则写好，不占特殊通道 —— 业务可以不用它们、
// 自定义等价模型。适用边界：固定查列 id / code / status，表的相应列不叫
// 这些名字时不要用（列不在集合会报错）。
// ---------------------------------------------------------------------------

// Range 是 util.Range[T] 的类型别名（定义只有一份，在 util；门面再导出，
// 不是第二实现）。左闭右开 [Min, Max)，边界零值 = 未给。
type Range[T any] = util.Range[T]

// IDQuery 按主键等值查询（id = 0 也是生效条件）。
type IDQuery struct {
	ID int64 `dbx:"id"`
}

// IDsQuery 按主键集合查询（切片推断 in；空切片 = 无条件）。
type IDsQuery struct {
	IDs []int64 `dbx:"id"`
}

// CodeQuery 按 code 等值查询。
type CodeQuery struct {
	Code string `dbx:"code"`
}

// StatusRequest 状态字段的部分更新载体（Update 投影；零值也是有效值）。
type StatusRequest[T any] struct {
	Status T `dbx:"status"`
}
