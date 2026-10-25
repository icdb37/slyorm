package dbwm

// ---------------------------------------------------------------------------
// 抽象数据类型（IR）
//
// IR 是纯数据：不含连接、不含方言引用、不含行为方法 —— 唯一的方法是不
// 导出的密封标记 isNode（只统一叶子与组的类型，不含逻辑）。上层构造它，
// dbwm 渲染成 SQL 并执行。校验在渲染期做，不在构造期做 —— 构造期没有
// 方言可依赖。
//
// 值一律走绑定参数，不出现在 SQL 文本里。
// ---------------------------------------------------------------------------

// Op 比较算子的语义名。渲染成什么 SQL 由算子表与方言决定。
type Op string

// 内置算子：eq、ne、gt、gte、lt、lte、like、in、nin、isnull、notnull。
const (
	OpEq      Op = "eq"
	OpNe      Op = "ne"
	OpGt      Op = "gt"
	OpGte     Op = "gte"
	OpLt      Op = "lt"
	OpLte     Op = "lte"
	OpLike    Op = "like"
	OpIn      Op = "in"
	OpNotIn   Op = "nin"
	OpIsNull  Op = "isnull"
	OpNotNull Op = "notnull"
)

// Node 条件树的一个节点（Composite 的组件接口）：叶子是 *Wcond，组是 *Wnode。
//
// isNode 不导出 —— 接口因此密封：包外造不出第三种节点，"既不是叶子也不
// 是组"的中间形态在类型上不可表达，渲染按类型分发（见 where.go）。
type Node interface {
	// isNode 是密封标记：不含行为，IR 仍是纯数据。
	isNode()
}

// Wcond 一条比较条件（叶子）：一列 + 一算子 + 一个值。
type Wcond struct {
	// Column 列名（原始值；引号由 dbwm 统一加，方言只给引用字符）。
	Column string
	// Op 比较算子。
	Op Op
	// Arg 参与比较的值：单值算子（eq/like…）放标量；in/nin 放集合
	// （任意切片类型，渲染时展开成 n 个绑定参数，空集合报错）；
	// isnull/notnull 忽略。
	Arg any
}

// isNode 实现 Node：Wcond 是叶子。
func (*Wcond) isNode() {}

// Comb 组内连接方式。
type Comb string

// 组内连接方式：and、or、not（空串按 and 处理）。
const (
	CombAnd Comb = "and"
	CombOr  Comb = "or"
	CombNot Comb = "not"
)

// Wnode 一组条件（组合节点）：组内按 Comb 连接，非顶层组整体用 () 包裹。
//
// 子节点仍是 Node —— 叶子与子组混排，递归成树；Items 里的 nil 项渲染期报错。
type Wnode struct {
	// Comb 组内连接方式；空串按 and 处理。
	Comb Comb
	// Items 组内的条件节点，按给定顺序连接。
	Items []Node
}

// isNode 实现 Node：Wnode 是组合节点。
func (*Wnode) isNode() {}

// AggFunc 聚合函数名。
type AggFunc string

// 聚合函数：count、sum、avg、min、max。
const (
	AggCount AggFunc = "count"
	AggSum   AggFunc = "sum"
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
)

// Agg 一个聚合列：渲染成 FUNC("列") AS "别名"。Query.Aggs 按指针持有。
type Agg struct {
	// Func 聚合函数。
	Func AggFunc
	// Column 聚合的列；为空只允许 count（渲染成 COUNT(*)）。
	Column string
	// Alias 输出别名：聚合结果在结果集里的唯一标识，扫描与 HAVING 都按它对齐。
	Alias string
}

// Paging 分页：怎么翻页（页码 + 每页条数）。
//
// 零值表示不限行数 —— Page 为 0 或 1、Size 为 0 时不渲染分页子句。
type Paging struct {
	// Page 页码，从 1 起；Size 为 0 时只能是 0 或 1。
	Page int
	// Size 每页条数；0 表示不限行数。
	Size int
}

// Query 一次查询的抽象描述：查什么 —— 表、列、条件、分组与聚合、排序。
//
// 不含分页 —— 取一行（Qryone）与全量（Qryall）用它；翻页用 PagedQuery（带 Paging）。
type Query struct {
	// Table 表名，支持 schema.table 带前缀形式。
	Table string
	// Columns 列清单，必须非空（不支持 SELECT *：列序要与扫描地址对齐）。
	Columns []string
	// Where 查询条件；nil 表示无条件。
	Where *Wnode
	// GroupBy 分组列；非空即渲染 GROUP BY。
	GroupBy []string
	// Aggs 聚合列；非空要求 GroupBy 非空。元素为指针，nil 项渲染期报错。
	Aggs []*Agg
	// Having 分组后的过滤；nil 表示无。
	Having *Wnode
	// Sorts 排序项：每项一个列名，前缀 - 倒序、+ 或无前缀正序
	// （如 "-id"、"created_at"）；有分组时只能是分组列或聚合别名。
	Sorts []string
}

// PagedQuery 带分页的查询描述：查什么 + 怎么翻页。
//
// 分页查询（Qrypgs）用它；Paging 为零值时不渲染分页子句（只发一条 COUNT）。
type PagedQuery struct {
	Query
	Paging
}

// Insert 一次插入的抽象描述。
type Insert struct {
	// Table 表名，支持 schema.table 带前缀形式。
	Table string
	// Columns 写入的列清单。
	Columns []string
	// Rows 每行的值，按 Columns 顺序。
	Rows [][]any
	// AutoPKColumn 需要取回生成值的自增列；空表示不取回。
	// 该列不得出现在 Columns 里。
	AutoPKColumn string
}

// Update 一次更新的抽象描述。
type Update struct {
	// Table 表名。
	Table string
	// Columns 要更新的列清单。
	Columns []string
	// Values 要写入的值，按 Columns 顺序。
	Values []any
	// Where 更新条件；为空报错（拒绝全表更新）。
	Where *Wnode
}

// Delete 一次删除的抽象描述。
type Delete struct {
	// Table 表名。
	Table string
	// Where 删除条件；为空报错（拒绝全表删除）。
	Where *Wnode
}

// Result 一次写入的结果。
type Result struct {
	// RowsAffected 影响行数。
	RowsAffected int64
	// IDs 本次由数据库生成的自增值，按行序；没有自增列时为空。
	// 写回模型字段是上层的活 —— dbwm 不认识模型。
	IDs []int64
}
