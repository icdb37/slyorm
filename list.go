package slyorm

// ---------------------------------------------------------------------------
// 分页协议类型（纯数据，不带 json/form 绑定标签 —— API 协议在业务自己的包里，
// 装配时字段拷贝转换；Keyword 之类的业务参数由业务映射，不进 slyorm）
// ---------------------------------------------------------------------------

// PageQuery 翻页与排序参数。
type PageQuery struct {
	// Page 页码，从 1 起；≤0 按 1 计；Size ≤0 时强制 1。
	Page int
	// Size 每页条数；≤0 不分页取全量。
	Size int
	// Sort 排序项："-col" 倒序、"+col"/"col" 正序（与 dbwm Sorts 同形）；
	// 列名必须在表模型的列集合里（白名单挡注入与拼错）。
	Sort []string
}

// ListRequest 分页列表请求：过滤条件 + 翻页参数。
type ListRequest[Q any] struct {
	PageQuery
	// Query 过滤条件模型；可为 nil（空条件，查全表）。
	Query *Q
}

// ListResponse 分页列表结果。
type ListResponse[T any] struct {
	// Total 过滤命中的总行数（与排序 / 分页无关；COUNT 与 SELECT 是两条
	// 语句、可能不同快照，要一致把调用包进 db.Tx）。
	Total int64
	// Data 当页数据；不分页时是全量。
	Data []*T
}
