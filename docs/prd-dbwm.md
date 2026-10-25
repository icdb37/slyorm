# 数据库访问中间层（dbwm）需求文档

> 本文定义 **dbwm 层**：SQL 抽象数据类型（IR）、方言、语句渲染、执行与扫描、连接与事务。
>
> 写规则见 [prd-insert.md](prd-insert.md)，读规则见 [prd-select.md](prd-select.md) —— 那两份讲的是**模型语义**（dbx 标签怎么解释）；本文讲的是**数据库落地**（抽象结构怎么变成 SQL、怎么执行）。
>
> 落地包：`dbwm/`（`package dbwm`，模块根下 —— `dml/` 层级已取消）。

## 目录

1. [目标与范围](#1-目标与范围)
2. [术语](#2-术语)
3. [分层与职责边界](#3-分层与职责边界)
4. [抽象数据类型（IR）](#4-抽象数据类型ir)
5. [需求条目 R1–R10](#5-需求条目)
6. [API 一览](#6-api-一览)
7. [与既有包的关系（改造清单）](#7-与既有包的关系改造清单)
8. [已知限制](#8-已知限制)
9. [已拍板](#9-已拍板)
10. [实现参考（文件结构）](#10-实现参考文件结构)

---

## 1. 目标与范围

### 1.1 目标

让上层（slyorm）**只描述"要做什么查询"，不碰 SQL 文本**；由 dbwm 把这份描述渲染成某个数据库认的 SQL 并执行。

- 上层产出的是**抽象数据类型**（表、列、条件树、排序、分组与聚合、分页、写入计划），不是字符串；
- 条件里的**值**一律走绑定参数，不拼进 SQL；
- **方言差异全部收在 dbwm**：标识符引用、占位符、算子关键字、分页写法、取回自增值的方式；
- dbwm 不认识 dbx：它只按接口（`RowFactory`）接收行目标，业务模型怎么映射由上层负责。

一句话概括：**上层给"意图"，dbwm 给"能跑在各数据库上的语句"。**

### 1.2 在范围内

| # | 范围项 |
| --- | --- |
| S1 | 查询抽象类型：表、列清单、条件树、排序、分组与聚合、分页 |
| S2 | 写入抽象类型：插入（单条/批量）、更新、删除 |
| S3 | 方言接口与三套实现（ANSI / MySQL / PostgreSQL） |
| S4 | 语句渲染：SELECT / COUNT / INSERT / UPDATE / DELETE |
| S5 | 执行与错误语义（无行、错误包装、参数顺序） |
| S6 | 行接收契约（`RowFactory`）与扫描校验 |
| S7 | 连接抽象、句柄构造、库型识别、事务 |

### 1.3 不在范围内

| 不在范围 | 去向 |
| --- | --- |
| JOIN、跨表条件 | 后续需求（会改变"列清单一一对应"等前提） |
| 聚合表达式、窗口函数、`FILTER`/`OVER` | 后续需求（聚合只到 COUNT/SUM/AVG/MIN/MAX + 列名） |
| 子查询、原始 SQL 片段拼接 | 不提供 —— 确实需要时注册自定义算子（见 R10）；总数统计内部包一层子查询，不对外暴露 |
| 字段级加密、软删、乐观锁等横切能力 | 后续需求（当前 WHERE 只能由上层构造） |
| Upsert / 冲突处理（`ON CONFLICT` / `ON DUPLICATE KEY UPDATE`） | 后续需求（各方言差异大，需要时按方言点设计） |
| 行级锁（`FOR UPDATE` / `FOR SHARE`） | 后续需求（事务内的锁定读，涉及隔离语义） |
| 连接池管理、驱动注册 | 属于调用方：本包不 import 任何驱动 |

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| **IR** | 抽象数据类型（Intermediate Representation）—— 方言无关的查询/写入描述，见 §4 |
| **方言** | 把 IR 渲染成某个库认的 SQL 的实现（见 R2） |
| **渲染** | 从 IR 生成 SQL 文本与绑定参数的过程（纯内存计算，无 IO） |
| **条件树** | 条件的有序树：叶子是 `Wcond`，内部节点是 `Wnode`，两者实现同一密封接口 `Node`（Composite） |
| **条件组** | `Wnode` —— 组内按 `Comb` 连接，整体用 `()` 包裹 |
| **顶层组** | `Query.Where` / `Query.Having` 直接指向的组 —— 不加外层括号（`not` 组的 `NOT (...)` 除外） |
| **算子** | 比较方式（`eq` / `like` / `in` …），语义名，方言决定它写成什么 SQL |
| **绑定参数** | 参与比较的值，在 SQL 文本里以占位符出现（形式由方言定：ANSI 是 `?`，PostgreSQL 是 `$1`），值本身由驱动绑定 |
| **行目标** | 一行结果的落点（通常是 `*T` 指针），由上层按行模型提供 |
| **列清单** | 查询要返回的列，普通列在前、聚合列在后，顺序与行目标的扫描地址顺序一一对应 |
| **分组列** | `GROUP BY` 的列清单 —— 分组维度 |
| **聚合项** | `Agg` —— 一个聚合列（`COUNT`/`SUM`/`AVG`/`MIN`/`MAX` + 列名 + 别名） |
| **排序项** | `Sorts` 的一个元素：列名前缀 `-` 倒序、`+` 或无前缀正序（如 `-id`、`created_at`） |
| **分页** | `Paging` —— `Page`（页码，从 1 起）+ `Size`（每页条数，0 = 不限） |
| **带分页的查询** | `PagedQuery` = `Query`（查什么）+ `Paging`（怎么翻页） |

---

## 3. 分层与职责边界

| 关注点 | 归属 | 例子 |
| --- | --- | --- |
| dbx 标签解析、列清单推导、行模型字段映射 | slyorm（`model/` / `query/` / `select/`（rows）） | `Person` 的 `Name` 字段对应 `name` 列 |
| 条件"参不参与"、算子适用性、**值成形** | slyorm | `"张"` → `"%张%"`；区间 → 下限 `gte` + 上限 `lt` |
| 算子 → SQL 写法（关键字、引号、占位符） | dbwm（算子表 + 方言） | `like` → `LIKE ?` —— 关键字是标准 SQL 由通用算子表给出，引用字符与占位符由方言给出，加引号由 dbwm 统一做 |
| 语句骨架（WHERE/HAVING 组合与括号、分组、聚合、分页、COUNT） | dbwm | `(a AND b) OR c` |
| 连接、事务、执行、扫描、错误语义 | dbwm | `ErrNoRows` |
| 标签文本处理、取值、零值判定、区间类型 | `util/` | `util.SnakeCase` |

**关键切口 —— 值成形在 slyorm，语法在 dbwm**：

- slyorm 决定"比什么、值是什么形状"：`like` 的值补好 `%`、区间只保留给了的那端、空切片直接不参与；
- dbwm 只决定"这条比较写成什么 SQL"：列名加引号、算子关键字、参数占位符。

因此 **slyorm 完全不需要方言**，也就不存在"方言在多个包里各有一份"的问题。

---

## 4. 抽象数据类型（IR）

### 4.1 查询 IR

**命名约定**：类型名取"它是什么"的准确词，不取缩写；同层概念用同一词根（条件树一族 `Node`/`Wcond`/`Wnode` 共用 `W` 前缀词根，与 `Qry*` 方法族同构；`Sorts`/`Agg` 管结果集形状）。

| 类型 | 职责 | 命名理由 |
| --- | --- | --- |
| `Op` | 比较算子的语义名 | 算子 = operation，标准叫法 |
| `Node` | 条件树的一个节点 | 叶子与组的公共接口（Composite）；密封 —— 包外造不出第三种节点 |
| `Wcond` | 一条比较（叶子） | where 条件；`W` 前缀标识 where 族（与 `Qry*` 方法族同构） |
| `Comb` | 组内连接方式（and/or/not） | combination |
| `Wnode` | 一组条件（组合节点） | where 条件树的组节点，组内按 `Comb` 连接；与 `Wcond` 同根 |
| `Sorts` | 排序项清单（字符串） | `-id` 倒序、`+id`/`id` 正序 —— 一个字符串说清"按哪列、往哪个方向排" |
| `Agg` | 一个聚合列 | aggregation |
| `Query` | 一次查询的抽象描述（查什么） | 保留 —— 它是数据结构，与 `Qryall`/`Qryone`/`Qrypgs` 这些操作方法不同层 |
| `Paging` | 分页参数（怎么翻页） | paging —— 与 `PagedQuery` 同根 |
| `PagedQuery` | 带分页的查询描述 | `Query` + `Paging`：分页查询（`Qrypgs`）用它；取一行与全量（`Qryone`/`Qryall`）用 `Query` |

```go
// Op 比较算子的语义名。渲染成什么 SQL 由方言决定。
type Op string

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
// isNode 不导出 —— 接口密封，包外造不出第三种节点。
type Node interface {
	isNode()
}

// Wcond 一条比较条件（叶子）：一列 + 一算子 + 一个值。
type Wcond struct {
	Column string // 列名（原始值；引号由 dbwm 统一加，方言只给引用字符）
	Op     Op
	Arg    any // 值：单值算子放标量；in/nin 放集合（任意切片，渲染时展开）；isnull/notnull 忽略
}

func (*Wcond) isNode() {} // Wcond 是叶子

// Comb 组内连接方式。
type Comb string

const (
	CombAnd Comb = "and"
	CombOr  Comb = "or"
	CombNot Comb = "not"
)

// Wnode 一组条件（组合节点）：组内按 Comb 连接，整体用 () 包裹。
type Wnode struct {
	Comb  Comb
	Items []Node // 叶子与子组混排，递归成树
}

func (*Wnode) isNode() {} // Wnode 是组合节点

// AggFunc 聚合函数名。
type AggFunc string

const (
	AggCount AggFunc = "count"
	AggSum   AggFunc = "sum"
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
)

// Agg 一个聚合列：渲染成 FUNC("列") AS "别名"。
type Agg struct {
	Func   AggFunc
	Column string // 空 = COUNT(*)；其余聚合函数为空 → 报错
	Alias  string // 输出别名，扫描地址按它对齐
}

// Query 一次查询的抽象描述：查什么。
type Query struct {
	Table   string
	Columns []string // 列清单，必须非空（不支持 SELECT *）
	Where   *Wnode   // nil = 无条件
	GroupBy []string // 分组列；非空即渲染 GROUP BY
	Aggs    []*Agg   // 聚合列；非空要求 GroupBy 非空
	Having  *Wnode   // 分组后的过滤；nil = 无
	Sorts   []string // 排序项：前缀 - 倒序、+ 或无前缀正序（如 "-id"、"created_at"）
}

// Paging 分页参数：怎么翻页。
type Paging struct {
	Page int // 页码，从 1 起
	Size int // 每页条数；0 = 不限行数
}

// PagedQuery 带分页的查询描述：查什么 + 怎么翻页。
type PagedQuery struct {
	Query
	Paging
}
```

**分页的表达**：`Paging` 的 `Page` 与 `Size` 描述"第几页、每页几条"，`offset = (Page-1)*Size` 由 dbwm 推导 —— 上层不用自己算偏移，offset 不是"用户概念"。`Size == 0` 表示不限行数（不渲染分页子句）。

**分页与查询描述分开**：`Query` 不带分页参数，取一行（`Qryone`）与全量（`Qryall`）用它；翻页用 `PagedQuery`（内嵌 `Query` 与 `Paging`，`Qrypgs` 收它）。这样"不翻页"与"翻页"在类型上就分开了 —— 不翻页的入口没有页码可填，也就没有"页码从 1 起"这类无从填起的参数。

**聚合的表达**：`Aggs` 描述"这一行除了分组列，还带哪几个聚合值"，每个聚合项自带**别名** —— 别名是分组结果的唯一标识，扫描地址与 `HAVING` 都按它对齐。`Aggs` 是指针切片：上层可以直接持有聚合项变量（改了字段就体现在渲染里），代价是**元素为 nil 时渲染期报错**（不静默跳过）。

**为什么没有 `range` 算子**：区间在 slyorm 侧展开成"一条 `gte` + 一条 `lt`"，放进一个 `Comb: and` 的 `Wnode` —— 括号是 `Wnode` 的天然属性，"区间不会被 `or` 组拆散"这条规则由结构保证，不需要特殊算子。IR 因此只有"算子 + 一个值"一种形状。

**算子与值形态**（`Wcond.Arg` 是单值字段；in/nin 的集合在渲染时展开成 n 个绑定参数）：

| 算子 | Arg 形态（由上层保证） |
| --- | --- |
| `eq` `ne` `gt` `gte` `lt` `lte` | 标量 |
| `like` | 字符串（通配符已补好） |
| `in` `nin` | 集合：任意切片类型（`[]any`、`[]string`…），至少 1 个元素 |
| `isnull` `notnull` | 忽略（布尔开关已在上层转成"加/不加"） |

### 4.2 写入 IR

```go
// Insert 一次插入的抽象描述。
type Insert struct {
	Table        string
	Columns      []string
	Rows         [][]any // 每行按 Columns 顺序
	AutoPKColumn string  // 需要取回生成值的自增列（空 = 不取回）
}

// Update 一次更新的抽象描述。
type Update struct {
	Table   string
	Columns []string
	Values  []any // 按 Columns 顺序
	Where   *Wnode
}

// Delete 一次删除的抽象描述。
type Delete struct {
	Table string
	Where *Wnode
}
```

零值语义、默认值、自增列"零值不参与"等规则属于**模型语义**，留在 slyorm；dbwm 只负责"把这张表这些列这些值写成 INSERT"。

**写入结果**（同样跨层传递的纯数据）：

```go
// Result 一次写入的结果。
type Result struct {
	RowsAffected int64   // 影响行数
	IDs          []int64 // 本次由数据库生成的自增值，按行序；没有自增列时为空
}
```

**自增值的边界**：dbwm **只负责取回**自增值（`IDs`），**不负责写回模型字段** —— 它不认识模型。写回是上层的活（slyorm 的 insert 层用反射把 `IDs[i]` 写进第 i 行的自增字段），见 R6.7。

### 4.3 IR 的不变量

1. **标识符不做本地校验**（表名、列名、别名）—— 方言的 `QuoteIdent` 引用会转义引号，注入面封死；写错或不存在的标识符由数据库报错暴露（错误带语句文本，见 R6.1）。**表名允许 `schema.table` 带前缀形式**（与 prd-insert 的 `TableName()` 约定一致），按 `.` 分段后每段单独引用；
2. `Wcond.Op` 必须是已注册算子（核心表或 `RegisterOperator` 注册的），`Wcond.Arg` 的形态必须与算子匹配（单值算子放标量；`in`/`nin` 放非空切片，支持任意切片类型，渲染时展开成 n 个绑定参数）—— 渲染期校验；
3. `Wnode.Comb` 只能是 `and` / `or` / `not`，**空串按 `and` 处理**（与读规则 `comb=` 的默认一致）；
4. **条件树节点**：`Node` 是密封接口（Composite）—— 叶子 `*Wcond` 与组 `*Wnode` 之外没有第三种节点，"两个都空 / 两个都给"的联合形态在类型上不可表达；`Items` 里的 nil 项（nil 接口或 nil 指针）渲染期报错；
5. **分页**：`Paging.Size > 0` 才分页；`Page` 从 1 起（`Size > 0` 时 `Page < 1` 报错）；`Size == 0` 时 `Page` 只能是 0 或 1；`offset = (Page-1)*Size` 由 dbwm 推导；**分页只出现在 `PagedQuery` 上** —— `Qryone`/`Qryall` 收 `Query`，不翻页（`Qryone` 的语句只带 `LIMIT 1`，见 R7.2）；
6. **聚合与分组**：`Aggs` 非空 → `GroupBy` 非空；`Having` 非空 → `GroupBy` 非空（没有分组维度的过滤应放 `Where`）；有 `GroupBy` → `Columns` 非空且每个分组列都在 `Columns` 里；`Aggs` 里**元素为 nil 报错**；`Agg.Column` 为空只允许 `count`；`Agg.Alias` **互不重复**、且不与 `Columns` 里的列名重名；
7. **排序**：`Sorts` 每项剥掉方向前缀（`-`/`+`）后不能是空列名；有 `GroupBy` 时还必须是分组列或聚合别名（分组结果里没有其它列）—— 渲染期校验；
8. **写入对齐**：`Insert.Rows` 非空且每行长度等于 `len(Insert.Columns)`；`Insert.AutoPKColumn` 非空时**不得**出现在 `Columns` 里（自增列要么交给数据库生成并取回，要么由上层写显式值，二者互斥）；`Update.Columns` 非空且 `len(Update.Values) == len(Update.Columns)`。

---

## 5. 需求条目

每条需求以 **需求 / 规则 / 验收** 三段式给出。

### R1 抽象数据类型（IR）契约

**需求**：上层与 dbwm 之间只通过 IR 交换信息，双方都不传 SQL 文本。

**规则**：

1. dbwm 的查询入口收 `*Query`（取一行 `Qryone` / 全量 `Qryall`）或 `*PagedQuery`（分页 `Qrypgs`），写入入口收 `*Insert` / `*Update` / `*Delete` —— **没有"传一段 SQL 字符串"的口子**；
2. IR 是**纯数据**：不含方法、不含连接、不含方言引用；
3. IR 里的值一律是绑定参数，**不出现在 SQL 文本里**；
4. IR 可跨方言复用 —— 同一个 `*PagedQuery` 可以分别交给 ANSI / MySQL / PostgreSQL 方言渲染；
5. IR 的字段是**导出的**（上层要构造它们），但**结构体不做行为** —— 校验在渲染期做，不在构造期做（构造期没有方言，也没有渲染器可依赖）。

**验收**：

- `dbwm` 包的公开 API 里不出现 `stmt string` / `sql string` 形参（连接层的内部实现除外）；
- 同一个 `*PagedQuery` 分别用 `dbwm.ANSI` 与 `dbwm.PostgreSQL` 渲染，得到的语句不同但语义相同。

### R2 方言接口

**需求**：数据库之间的一切差异收在方言里，dbwm 的其余部分不出现 `if mysql` 这类判断。

**规则**：

1. 方言负责五类事：**标识符引用字符**、**占位符**、**分页写法**、写侧的**取回生成值**、**单语句参数上限**；标识符的加引号与转义（引号字符 doubling）由 dbwm 统一做（`dbwm.QuoteIdent(d, ident)`），方言不拼字符串；
2. 渲染流程中，**凡是标准 SQL 覆盖得到的部分不进方言** —— 条件组的括号、`AND`/`OR`/`NOT` 的连接、算子关键字（`=` `<>` `LIKE` `IN` `IS NULL`…）、`ORDER BY` 的写法、`COUNT(*)`、`INSERT INTO … VALUES` 的骨架都是标准 SQL，由 dbwm 的通用拼装器负责；将来真出现方言差异（如 `NULLS FIRST/LAST` 的排序），再把对应方法提进方言；
3. **占位符由渲染器统一分配编号** —— 渲染路径（算子、INSERT / UPDATE 的值）向渲染器要占位符，拿到的就是方言的形式（PostgreSQL 的 `$1、$2…`），编号在整条语句里连续（见 R5）；
4. 方言实现采用"基础实现 + 嵌入覆盖"：ANSI 是基础，MySQL / PostgreSQL 嵌入 ANSI 只覆盖差异点；
5. 方言是**并发安全**的只读对象（嵌入的空结构体），可被多个句柄共享。

```go
type Dialect interface {
	Name() string
	QuoteChar() string // 标识符引用字符：ANSI/PostgreSQL 是 "，MySQL 是 `
	Placeholder(n int) string // 第 n 个占位符：ANSI 是 ?，PostgreSQL 是 $n
	Limit(limit, offset int) string
	Returning(column string) string
	BatchAutoIDs(lastInsertID int64, n int) ([]int64, bool)
	MaxParams() int
}

// 加引号与转义统一在 dbwm：方言只给引用字符
func QuoteIdent(d Dialect, ident string) string
```

`MaxParams` 是**一条语句里绑定参数的上限**，批量插入据此分块（见 R6.5）：SQLite 32766（3.32 起；旧版是 999，主流驱动都已内置新引擎）、MySQL 65535、PostgreSQL 65535。要兼容更老的库，注入自定义方言调小该值即可。

**验收**：

- `dbwm.QuoteIdent(dbwm.MySQL, "name")` → `` `name` ``，ANSI → `"name"`（方言给 `QuoteChar`，加引号与转义统一在 dbwm）；
- PostgreSQL 的 `Placeholder(2)` → `"$2"`（编号由渲染器连续分配，见 R5）；
- `Limit(10, 20)` → `LIMIT 10 OFFSET 20`（三套方言同形；不支持分页的库返回空串 → 渲染期报错）；
- `MaxParams()`：ANSI 32766，MySQL / PostgreSQL 65535；
- 换方言只改 `dbwm.New(...)` 的构造选项，dbwm 内部代码不变。

### R3 条件渲染与括号规则

**需求**：条件树的 SQL 形态必须可预期，与数据无关。

**规则**：

1. **叶子**渲染成 `列 算子 参数`（`isnull` / `notnull` 是 `列 IS NULL` / `列 IS NOT NULL`，不绑参数）；
2. **组**的形态固定：
   - `and` 组 → `(a AND b)`；
   - `or` 组 → `(a OR b)`；
   - `not` 组 → `NOT (a AND b)`（组内按 AND 连接后整体取反）；
3. **顶层组**（`Query.Where` / `Query.Having` 直接指向的组）不加外层括号，`WHERE`/`HAVING` 后面直接跟条件 —— 唯一的例外是 `not` 组，它渲染成 `NOT (...)`，括号是 `NOT` 形态的一部分；其余组一律 `()` 包裹，**不因连接方式或子条件数量而变**（只有一个子条件的组也照常包裹）；
4. **空组不参与** —— 子条件全部不存在的组连括号一起消失；
5. **参数顺序 = 条件树从左到右的遍历顺序** —— 深度优先、先叶子后兄弟，与 SQL 文本里 `?` 的出现顺序一一对应；
6. 值形态与算子不匹配 → **渲染期报错**（不是运行期 SQL 错误）：`Wcond{Op: OpIn, Arg: []any{1}}` 合法；`Wcond{Op: OpIn, Arg: 1}`（标量不是集合）、`Wcond{Op: OpIn, Arg: []any{}}`（空集合）报错。

**验收**：

- 顶层两个条件 → `a = ? AND b = ?`（无外层括号）；
- 顶层 `or` 组 → `a OR b`（不加外层括号）；顶层 `not` 组 → `NOT (a AND b)`；
- 组内子条件全为空 → 该组在 SQL 里不出现。

### R4 语句渲染

**需求**：语句的形态确定，方言只改差异点。

**规则**：

1. **SELECT**：`SELECT <列清单> FROM <表> [WHERE <条件>] [GROUP BY <分组列>] [HAVING <条件>] [ORDER BY <排序>] [LIMIT n OFFSET m]`；
   - 列清单 = **普通列在前、聚合列在后**：普通列按 `Columns` 顺序，聚合列按 `Aggs` 顺序；每项按方言引用，逗号分隔；
   - **`Columns` 不能为空 —— 不支持 `SELECT *`**，为空渲染期报错（`SELECT *` 的列序由数据库决定，与扫描地址无法对齐）；
   - 聚合项渲染成 `FUNC("列") AS "别名"`；`Agg.Column` 为空时是 `COUNT(*)`；
   - 条件为空（`Where == nil` 或渲染后为空）→ **不拼 `WHERE` 关键字**（不要 `1=1` 那种骨架）；
   - 表名支持 `schema.table` 带前缀形式，按 `.` 分段逐段引用（`"public"."person"`），与 prd-insert 的 `TableName()` 约定一致 —— INSERT / UPDATE / DELETE 同；
2. **GROUP BY / HAVING**：
   - `GroupBy` 非空 → 渲染 `GROUP BY "列1", "列2"`（按给定顺序）；
   - **`GroupBy` 的每个列必须出现在 `Columns` 里** —— 否则报错（结果集要先给出分组键，扫描地址才对得上）；
   - `Having` 非空 → 渲染 `HAVING <条件>`，与 `WHERE` 用同一套 `Wnode` IR、同一套括号规则；`Having` 非空而 `GroupBy` 为空 → **报错**（没有分组维度，过滤条件应放 `Where`）；
   - `Aggs` 非空而 `GroupBy` 为空 → **报错**（聚合没有分组维度）；
   - `Aggs` 里**有 nil 元素 → 报错**（聚合项缺失是调用方的错，不静默跳过）；
   - **`HAVING` 里的列名只能是分组列或聚合项的别名**，引用其它列名渲染期报错；
   - **聚合别名在 `HAVING` 里渲染成聚合表达式本身**（`cnt` → `COUNT(*)`）—— PostgreSQL 的 `HAVING` 不认输出别名（MySQL / SQLite 认），改写成表达式后三库通吃；上层 IR 仍然只写别名，改写由渲染器完成。`ORDER BY` 不需要改写：三库都认输出别名，直接按别名排；
3. **COUNT（总数）**：
   - 无 `GroupBy` → `SELECT COUNT(*) FROM <表> [WHERE <条件>]`；
   - 有 `GroupBy` → 总数是**分组数**，渲染 `SELECT COUNT(*) FROM (SELECT <列清单> FROM <表> [WHERE] [GROUP BY] [HAVING]) <子查询别名>` —— 内层与外层查询用同一份列清单与条件，保证 `HAVING` 的聚合在内层可见；**子查询不带 `ORDER BY` 与 `LIMIT/OFFSET`**（它们不改行数，带进去只会浪费，`LIMIT` 带进去还会把总数数成页大小）；
   - 标准 SQL，不进方言；
4. **INSERT**：`INSERT INTO <表>(<列清单>) VALUES (?,?),(?,?)`；
   - 多行一个语句（批量），行数按参数上限自动分块（见 R6.5）；
5. **UPDATE**：`UPDATE <表> SET <列>=? … [WHERE <条件>]`；
6. **DELETE**：`DELETE FROM <表> [WHERE <条件>]`；
7. **UPDATE / DELETE 的条件为空 → 报错** —— 全表更新与全表删除是误操作的主要来源，宁可报错也不放行；
8. **分页**：`Paging.Size > 0` 时渲染分页子句，`offset = (Page-1)*Size`；`Size == 0` → **不渲染分页**（不限行数）；
   - **`Size > 0 && Page < 1` → 报错**（页码从 1 起）；**`Size == 0 && Page > 1` → 报错**（没有每页条数算不出偏移）；
   - 分页子句的写法由方言给出（`Limit` 方法返回空串表示该库不支持分页 → 报错）；
   - **`Qryone` 取一行不翻页**：语句只带 `LIMIT 1`，不带偏移 —— 取一行的入口收 `Query`（不含分页参数），见 R7.2；
9. **排序**：`Sorts` 非空时渲染 `ORDER BY "列" [DESC] …`（每项前缀 `-` 倒序、`+` 或无前缀正序）；列名按方言引用；**多列按给定顺序**；排序列可以是**聚合项的别名**（三库都认输出别名）；有 `GroupBy` 时排序列必须是分组列或聚合别名，其余渲染期报错（见 §4.3）；
10. **空写入报错**：INSERT 的列清单为空、`Rows` 为空（没有可插入的行 —— 上层对空切片提前返回，见 prd-insert R9.6）、UPDATE 的列清单为空，都渲染期报错。

**验收**：

- `Query{Table: "person", Columns: []string{"id", "name"}}` → `SELECT "id","name" FROM "person"`；
- `Update{Table: "person", Where: nil}` → 报错，错误信息指出"缺少 WHERE"；
- `PagedQuery{Query: Query{Table: "person"}, Paging: Paging{Size: 0, Page: 2}}` → 报错（没有每页条数算不出偏移）；
- `Query{Table: "person", Columns: []string{"dept"}, GroupBy: []string{"dept"}, Aggs: []*Agg{{Func: AggCount, Alias: "cnt"}}}` → `SELECT "dept", COUNT(*) AS "cnt" FROM "person" GROUP BY "dept"`。

### R5 占位符与参数顺序

**需求**：值一律走绑定参数，且参数与占位符严格对应。

**规则**：

1. IR 的值**只出现在 `Arg` 里**，SQL 文本里只有占位符；
2. **占位符由渲染器分配编号**：渲染路径（算子、INSERT / UPDATE 的值）向渲染器要占位符，拿到的就是方言的形式（PostgreSQL 的 `$1、$2…`），编号在整条语句里连续；
3. **分配顺序 = 占位符在文本里的出现顺序** —— 由渲染顺序保证：SELECT 是 `WHERE` 条件 → `HAVING` 条件，UPDATE 是 `SET` 值 → `WHERE` 条件，INSERT 是按行、行内按列；编号与参数因此严格同步；
4. **片段的占位符数必须与该片段的参数个数一致** —— 渲染期自检，不一致直接报错（内置算子的占位符由渲染器分配，天然一致；自定义算子必须遵守，见 R10.3）；
5. **渲染器是单条语句私有的**：一次渲染一个实例，不跨 goroutine 共享；算子拿到的渲染器**只能在本次调用内使用**（不得留存到别处、不得并发调用）—— 因此多个 goroutine 并发渲染同一个 `PagedQuery` 互不影响；
6. **分页的 limit/offset 内联为整数**（`LIMIT 10 OFFSET 20`），不占占位符 —— 免去占位符续号的问题，也避免各库对"无上限"写法的差异。

**验收**：

- 条件有 2 个参数、PostgreSQL 方言 → SQL 里是 `$1`、`$2`，且 `args` 顺序与条件顺序一致；
- UPDATE 的 SET 值与 WHERE 条件、PostgreSQL 方言 → `$1、$2、$3`（SET 在前，编号跟着文本顺序走）；
- 一个自定义算子返回的片段里 `?` 多于参数个数 → 渲染期报错（不是把 SQL 交给驱动后才炸）。

### R6 执行与错误语义

**需求**：出错了要能立刻定位，且"没有数据"与"出错"分得开。

**规则**：

1. 所有错误信息以 **`dbwm: `** 开头，包装底层错误一律用 `%w`（保证 `errors.Is` / `errors.As` 可穿透）；错误信息要指出**哪条规则**不满足（哪个字段、哪个算子、哪条语句）；
2. **没有行**用哨兵错误 `ErrNoRows` 表示，调用方可用 `errors.Is` 判定；
3. `Qryall` 查不到行**不是错误** —— 返回空结果；只有 `Qryone` 把"没有行"当错误；
4. 事务内出错 → 回滚；`fn` 返回错误 → 回滚并原样返回该错误；`fn` panic → 先回滚再把 panic 抛回；
5. **批量插入按参数上限分块**（上限由方言 `MaxParams` 给出：SQLite 32766、MySQL / PostgreSQL 65535，见 R2）—— 超限时拆成多条语句执行；`RowsAffected` 是各块之和，自增值按块取回后按行序拼接；**分块产生的多条语句不自动包事务** —— 要么由上层包 `db.Tx`，要么接受部分成功；
6. 执行前**校验目标数与实际列数一致**（见 R8.3），不一致直接报错，不把错位的扫描交给驱动；
7. **自增值由 dbwm 取回、由上层写回**：插入返回 `Result{IDs, RowsAffected}`，`IDs` 是本次由数据库生成的自增值，按行序对应 `Rows`；dbwm 不做字段回填（它不认识模型，见 §4.2 的边界说明）；
   - 方言推算不可靠时（如 MySQL 多行 INSERT 的 `LastInsertId` 只给第一行）→ **报错**，不返回可能错位的 ID；
   - 走 `RETURNING` 取回时（PostgreSQL）`RowsAffected` = 取回的行数；`LastInsertId` 路径照常取 `sql.Result`；
   - 没有自增列时 `IDs` 为空。

**验收**：

- `Qryone` 未命中 → `errors.Is(err, dbwm.ErrNoRows)` 为真；
- 驱动返回的唯一性冲突 → 错误里能看到原始驱动错误（`%w`）；
- 批量插入 2000 行 × 3 列 → 自动拆成多条语句，不触发驱动参数上限错误；
- 插入单行自增主键 → 返回的 `IDs` 长度为 1，值等于库里的自增值。

### R7 查询接口

**需求**：三种查询形态语义清晰、返回值统一。

**规则**：

0. 查询入口是 **DB 的方法（非泛型）** `Qryall(ctx, *Query, factory)` / `Qryone(ctx, *Query, factory)` / `Qrypgs(ctx, *PagedQuery, factory)`，不返回行数据 —— 行对象由 RowFactory 的实现在 `NewRow` 时创建并收集（见 R8）；**泛型在 slyorm 层**：泛型工厂包装 dbwm 调用，业务侧直接拿到 `[]*T`；
1. `Qryall` —— 全量查询，返回全部行（不翻页、不发 COUNT —— 要翻页连带总数用 `Qrypgs`）；
2. `Qryone` —— 最多一行；**没有行返回 `ErrNoRows`**，多于一行取第一行（不报错，多行是上层条件没写够）；**入口收 `Query`（不含分页参数），取一行不翻页** —— 语句只带 `LIMIT 1`（数据库只算一行），不带偏移；取到第一行后立即关闭结果集；
3. `Qrypgs` —— 分页查询，**返回总行数**（总数由 dbwm 另发一条 COUNT 得到，见 R4.3），本页行从工厂取；
4. 三个方法**返回值形态统一**：`Qryall` / `Qryone` 只返回 `error`（行数据本就从工厂收集，"没有行"由 `ErrNoRows` 表达），`Qrypgs` 返回 `(total, error)`；行数据从工厂取 —— slyorm 层的泛型工厂在 `NewRow` 里 `new(T)`、收集进 `[]*T`、返回字段地址，业务侧因此直接拿到 `[]*T`，不需要断言；
5. **COUNT 与 SELECT 是两条语句，不在同一快照** —— 两者之间数据可能变化；需要一致时把调用包进 `db.Tx` 里（同一事务内）；
6. 分页参数由 `Paging` 自带（`PagedQuery` 内嵌 `Query` 与 `Paging`）；`Qrypgs` 不额外接受分页参数；
7. 排序与分组在 `Query` 里描述、分页在 `Paging` 里描述，翻页一律走 `Qrypgs`（连带一条 COUNT）；
8. **有分组时，`Qrypgs` 的 `total` 是分组数**，不是行数（见 R4.3）。

**验收**：

- `Qrypgs` 返回的 `total` 与库里该条件下的行数一致；
- `Qryone` 未命中 → `ErrNoRows`；
- 不带 `Where` 的 `Qryall` → 查全表（允许，但上层应避免；危险写操作由 R4.7 拦住）。

### R8 行接收（RowFactory）

**需求**：dbwm 不认识 dbx，但仍要把一行数据准确交给上层。

**规则**：

1. 上层提供 `RowFactory`：`NewRow` **创建行对象、收集进结果列表，返回该行各列的扫描地址**：

   ```go
   type RowFactory interface {
       NewRow() []any // 创建行对象（如 *Person）并收集，返回各列扫描地址，顺序与 Query.Columns + Query.Aggs 一致
   }
   ```

   dbwm 每行 `NewRow` 拿地址 → 交给驱动 `Scan` 直接写入 —— 扫描完成的行对象已经落在实现侧的列表里，查询结束直接取用；

2. **扫描由 `database/sql` 做** —— dbwm 把 `Targets` 原样交给 `rows.Scan`，不做类型转换（时间、JSON、`sql.Null*` 的解码规则归驱动）；
3. **扫描前校验**：实际返回的列数（`rows.Columns()`）必须等于 `len(Targets)`，不等直接报错 —— 错位的扫描会静默写错字段；
4. 列的**顺序**由 `Query.Columns` + `Query.Aggs` 决定 —— 普通列在前、聚合列在后（渲染顺序即结果集顺序）；`Columns` 必须非空（不支持 `SELECT *`，见 R4.1）；
5. **聚合列按别名对位** —— 聚合项的别名是它在结果集里的唯一标识，上层用别名（而不是列名）把聚合值扫进哪个字段；
6. dbwm **不认识行目标** —— 它只把扫描地址交给驱动、汇报行数；行对象的类型、创建与收集都在工厂实现侧。**泛型工厂由 slyorm 层实现**（`NewRow` 里 `new(T)`、收集进 `[]*T`、返回字段地址），业务侧直接拿到 `[]*T`。

**验收**：

- `Query.Columns` 是 3 列而 `Targets` 只给了 2 个 → 报错，错误信息指出列数不符；
- `sql.NullString` 列扫进 `*sql.NullString` 目标 → 由驱动处理，dbwm 不插手。

### R9 连接与事务

**需求**：连接与事务是数据库层的关注点，收口在 dbwm。

**规则**：

1. `Conn` / `Tx` 接口**归 dbwm 所有**，`*sql.DB` / `*sql.Tx` 由适配器实现（`sqlConn` / `sqlTx`）；
2. **本包不 import 任何驱动** —— 驱动由调用方注册（`_ "modernc.org/sqlite"`）；`Open(driver, dsn, opts...)` 只是按名字 `sql.Open`；
3. 构造入口：
   - `Open(driver, dsn, opts...)` —— 按驱动名映射方言（`sqlite` → ANSI，`mysql` → MySQL，`postgres`/`pgx` → PostgreSQL）；映射不到就**连库探测**（`version()` / `sqlite_version()`），认不出来**报错**（不猜）；
   - `New(conn, opts...)` —— 调用方自带连接（依赖注入入口，测试用替身走这条）；
   - `NewSQL(base, opts...)` —— 调用方自带 `*sql.DB`（连接池由调用方负责）；
4. **方言是构造选项**：`Option`（见 `dbwm.WithDialect`）是改句柄构造期配置的函数，参数类型未导出（包外只能用本包给的选项函数）；**不给选项时用标准 SQL 方言（ANSI）**。`Open` 里显式指定的方言**优先于**驱动名映射与连库探测（给了方言就不必探测）；
5. **事务用 ctx 传递**：`db.Tx(ctx, fn)` 里 `fn` 收到的 ctx 自动带上事务，期间的所有 dbwm 调用跑在同一事务里；**嵌套调用复用外层事务**，由最外层提交或回滚；**事务结束后该 ctx 上的数据库调用报错**（提交/回滚都算结束）—— 绝不静默改走连接池，否则"以为在事务里"的写会悄悄落到池上；
6. **不提供"取回底层 `*sql.DB`"的逃生舱** —— 注入连接的一方自己留着句柄；
7. `Close` 只关闭 `Open` 打开的连接；`New` 注入的连接所有权在注入方，dbwm 不代关；
8. 建表、报表等**手写 SQL** 由调用方在自己的程序里执行（本包只提供 ORM 能力，不做 DDL）；唯一例外是 `ExecScript`（R11）—— 迁移脚本（DDL / 初始化）经此窄口子执行，见 [prd-migration.md](prd-migration.md) R6。

**验收**：

- `db.Tx` 里的 `db.Qryall` 与 `db.Tx` 外的 `db.Qryall` 走不同的事务边界；
- 嵌套 `db.Tx` 不新开事务；
- `Open("unknown", dsn)` 且探测不出库型 → 报错，错误信息提示显式指定方言；
- `Open("unknown", dsn, dbwm.WithDialect(dbwm.MySQL))` → 用 MySQL 方言，不探测；
- `dbwm.New(conn)` 不给选项 → 方言是 ANSI；
- 事务回调返回后，再用回调里的 ctx 执行查询 → 报错（不落回连接池）。

### R10 算子扩展

**需求**：核心算子表不够用时，业务能自己加。

**规则**：

1. 注册入口 `dbwm.RegisterOperator(name, fn)` —— 新增算子名（`contains` / `startswith` / 真·正则…）；
2. 算子实现的职责：**给条件与方言，产出 SQL 片段与绑定参数**；片段里的占位符写 `?`（渲染时由 dbwm 换成渲染器分配的方言占位符，编号在整条语句里连续，见 R5）；
3. **自定义算子必须遵守**：片段里的 `?` 数与返回的参数个数一致（否则渲染期报错）；片段的字符串字面量里**不要出现 `?`**（占位符按文本计数，见 R5.4）—— 要传含 `?` 的文本，走绑定参数；
4. **算子名不可覆盖**：注册已存在的算子名（核心表里的或先注册的）→ 报错 —— 全局表被悄悄改写 `eq` 的语义没法排查；
5. 注册必须在该算子**第一次被使用之前**完成 —— 算子表是全局的、并发安全的（读写用锁）；
6. **算子的"模型层适用性"由上层判断**（`like` 只能配字符串字段、`isnull` 只能配布尔字段这类规则留在 slyorm）；dbwm 只管"怎么写"；
7. 自定义算子的**方言差异**由算子实现自己处理（拿得到方言）。

**验收**：

```go
dbwm.RegisterOperator("contains", func(d dbwm.Dialect, c *dbwm.Wcond) (string, []any, error) {
	return dbwm.QuoteIdent(d, c.Column) + " LIKE ?", []any{"%" + fmt.Sprint(c.Arg) + "%"}, nil
})
```

注册后 `Wcond{Column: "note", Op: "contains", Arg: "x"}` 可用。

### R11 脚本执行（ExecScript）

**需求**：迁移能力（[prd-migration.md](prd-migration.md)）要按水位执行业务手写 SQL 脚本，本包提供原样执行的唯一入口。

**规则**：

1. `ExecScript(ctx, script string) error`：把 `script` 原样交给驱动执行 —— 不渲染、不拆分、无绑定参数；返回值只有 error（多语句的 RowsAffected 不可靠，不暴露）；
2. 走 ctx 绑定的执行器（R9.4）：ctx 带事务即在事务内执行；
3. 空白文本直接返回 nil、不发语句（MySQL 对空查询报 1065）；
4. 错误：`dbwm: 执行脚本失败: %w` —— **不含脚本正文**（可能极长，脚本定位由上层带文件名），是 R6.1"错误信息带语句文本"的例外；
5. 这是 §8.10"没有原始 SQL 逃生舱"与 R9.8"不做 DDL"的**窄修订**：业务查询绕过 IR 拼条件的口子仍然不开；脚本经此唯一入口执行（无参数、命名即语义）。

**验收**：

- ctx 带事务时语句在事务内执行（事务回滚则脚本效果一并消失）；
- 空白文本不发语句、返回 nil；
- 驱动错误可 `errors.Is` / `errors.As` 穿透。

---

## 6. API 一览

```go
import "slyorm/dbwm"

// 句柄
db, err := dbwm.Open("sqlite", ":memory:")   // 探测库型 → 选方言
db := dbwm.New(conn)                         // 注入连接（方言默认 ANSI）
db := dbwm.New(conn, dbwm.WithDialect(dbwm.MySQL)) // 注入连接与方言
db := dbwm.NewSQL(base, dbwm.WithDialect(dbwm.PostgreSQL)) // 注入 *sql.DB
db.Dialect()                                  // 当前方言
db.Close()
db.Tx(ctx, func(ctx context.Context) error { // 事务（ctx 传递，嵌套复用）
	return nil
})

// 脚本执行（迁移能力用，见 R11）：原样执行 DDL / 初始化脚本
err = db.ExecScript(ctx, script)               // 多语句整发；空白即 no-op

// 查询（dbwm 层，非泛型）：上层给 IR，dbwm 渲染并执行，行从工厂收集
err := db.Qryall(ctx, &dbwm.Query{
	Table:   "person",
	Columns: []string{"id", "name"},
	Where:   where,      // 上层构造的条件树
	Sorts:   []string{"-id"},                    // id 倒序；"+id"/"id" 正序
}, factory)             // factory 的 NewRow 创建行对象并收集、返回字段地址

err = db.Qryone(ctx, q, factory)             // q 是 *dbwm.Query；没有行 → dbwm.ErrNoRows
total, err := db.Qrypgs(ctx, pq, factory)    // pq 是 *dbwm.PagedQuery；总数是分组数时见 R4.3

// 分组查询：分组列 + 聚合列，聚合值按别名对位
total, err = db.Qrypgs(ctx, &dbwm.PagedQuery{
	Query: dbwm.Query{
		Table:   "person",
		Columns: []string{"dept"},
		GroupBy: []string{"dept"},
		Aggs:    []*dbwm.Agg{{Func: dbwm.AggCount, Alias: "cnt"}},
		Having:  having,   // 引用聚合别名（如 "cnt" > 10）；渲染时改写成聚合表达式，见 R4.2
		Sorts:   []string{"-cnt"},
	},
	Paging: dbwm.Paging{Page: 1, Size: 20},
}, factory)

// 泛型包装在 slyorm 层（select/，包名 rows）：工厂里 new(T)、收集、返回字段地址，
// 业务侧直接拿到 []*Person —— dbwm 保持非泛型
var rows []*Person
f := dbwm.RowFactoryFunc(func() []any {
	p := new(Person)
	rows = append(rows, p)
	return []any{&p.ID, &p.Name} // 与 Query.Columns 顺序一致
})
err = db.Qryall(ctx, q, f)
// rows 里是扫描完成的 *Person

// 写入
res, err := db.Insert(ctx, &dbwm.Insert{
	Table:        "person",
	Columns:      []string{"name", "age"},
	Rows:         [][]any{{"张三", 30}},
	AutoPKColumn: "id",
})
// res.IDs 是数据库生成的自增值（按行序）—— 由上层写回模型字段，dbwm 不做回填
res, err = db.Update(ctx, &dbwm.Update{
	Table:   "person",
	Columns: []string{"age"},
	Values:  []any{31},
	Where:   cond,
})
res, err = db.Delete(ctx, &dbwm.Delete{Table: "person", Where: cond})
```

- 错误一律以 `dbwm: ` 开头；"没有行"用 `dbwm.ErrNoRows`；
- 条件树、条件组、条件、排序项、聚合项由上层构造（字段全是导出的，构造不做校验，校验在渲染期）；
- 分页由 `Paging.Page`（从 1 起）与 `Paging.Size`（0 = 不限）表达，offset 由 dbwm 推导；取一行用 `Query`（不带分页参数）；
- 插入返回的 `Result.IDs` 由**上层**写回模型字段 —— dbwm 不认识模型。

---

## 7. 与既有包的关系（改造清单）

dbwm 落地后，以下几处要改（分两轮，先查询后写入）。第一轮期间 `util.Dialect` 与 dbwm 方言**短暂并存**（insert 还没迁移），第二轮 insert 迁到 dbwm 后删掉 util 里的方言与探测。

> **改造已全部落地**（见 [prd-slyorm.md](prd-slyorm.md) §7）：`dml/` 层级取消、能力包上移模块根；下表"现状"列是**改造前**的历史记录（旧路径仅存于此），"改造后"列即当前实况。

| 包 | 现状 | 改造后 |
| --- | --- | --- |
| `dml/util`（旧 `dml/` 层级，改造时的现状） | 有 `Dialect` 接口 + ANSI/MySQL/PostgreSQL 实现 + `Detect` + 驱动名映射 | **方言并入 `dbwm/`**（重复实现删除）；util 只留标签文本、取值、零值判定、区间类型 ✅ |
| `dml/insert`（现 `insert/`） | 自己定义 `Conn`/`Tx`/`sqlConn`/`sqlTx`；`plan.go` 生成 INSERT 语句；自增值直接反射写回模型 | 连接抽象**改用 dbwm 的**；产出 `*dbwm.Insert` / `*dbwm.Update` IR，SQL 由 dbwm 渲染；自增值改为**取 dbwm 返回的 `Result.IDs` 再写回模型** ✅ |
| `dml/query`（现 `query/`） | 算子产出 SQL 片段（带引号、带占位符）；`BuildWhere` 需要方言 | 算子改为**语义 + 值成形**，产出 `*dbwm.Wnode`；**不再需要方言** ✅ |
| `dml/model`（现 `model/`，已落地） | —— | 承载**静态列集合（列名 + 路径 + 选项）+ 主键标记**，insert 与 select 共用；表名推导留在 insert（门面显式传表名） ✅ |
| `select/`（包名 `rows`，已落地） | —— | 行模型解析 → 列清单 + `RowFactory` 实现；扫描目标构造（NULL 兜底 / 时间 / JSON）；返回 `[]*T` ✅ |
| `dml/dbwm`（现 `dbwm/`，已按本文重写） | `querier.go` / `exector.go` 用接口把 **SQL 文本**跨层传递（`ReqSelector.Filter() (string, []any)`） | 按本文重写：跨层只传 IR（条件树、列清单、排序、分组、分页），SQL 文本只在 dbwm 内部生成；查询方法名用 `Qryall` / `Qryone` / `Qrypgs` |

---

## 8. 已知限制

1. **只覆盖单表查询**：JOIN、跨表条件、用户侧子查询不在范围（总数统计内部包一层子查询，不对外暴露）；
2. **聚合只有固定几个函数**：COUNT/SUM/AVG/MIN/MAX —— 没有表达式、没有 `FILTER`/`OVER`、没有窗口函数；
3. **HAVING 只能引用分组列与聚合别名** —— 不能写 `COUNT(DISTINCT "id") > 1` 这类任意聚合表达式；别名在渲染时改写成聚合表达式（PostgreSQL 的 `HAVING` 不认输出别名，见 R4.2），上层 IR 始终只写别名；
4. **排序只到列名**：不支持 `ORDER BY created_at DESC NULLS LAST` 这类带表达式的排序（各方言的 `NULLS` 语法不同，将来作为方言点补）；
5. **COUNT 与 SELECT 不在同一快照**：分页查询的总数与本页数据可能不一致，需要一致时包进事务；
6. **`Qryone` 多行时取第一行**：不报错（上层条件写宽了不会被拦下）；
7. **分页要成对给**：`Paging.Size == 0` 表示不限行数，此时 `Paging.Page` 只能是 0 或 1（避免各家"无上限"写法差异）；
8. **列清单与扫描地址靠上层对齐**：dbwm 只校验数量，不校验列名；
9. **不支持 `SELECT *`**：`Columns` 必须非空，为空渲染期报错 —— `SELECT *` 的列序由数据库决定，与扫描地址无法对齐；
10. **没有原始 SQL 逃生舱**：确实要写原生 SQL 的场景由调用方在自己的连接上执行 —— 本包不提供拼接口子（避免注入面）；窄修订：迁移脚本（DDL / 初始化）经 `ExecScript`（R11）执行，业务查询绕过 IR 拼条件的口子仍然不开（见 prd-migration）；
11. **`LIKE` 的大小写敏感性随库而异**：SQLite / MySQL 默认不区分 ASCII 大小写，PostgreSQL 区分 —— dbwm 不抹平这个语义差异，需要一致行为的业务在上层自行处理（如统一小写化后比较）；
12. **自增值只支持 `int64`**（`LastInsertId` 的语义）：数据库生成的字符串 / UUID 主键不在取回范围内，这类主键由调用方自己赋值。
13. **标识符不做本地校验**：表名 / 列名 / 别名写错（包括注入尝试）不在渲染期拦截 —— 方言引用的转义保证它只可能是"不存在的标识符"，最终由数据库报错暴露（错误带语句文本，见 R6.1）。

---

## 9. 已拍板

| # | 议题 | 结论 | 章节 |
| --- | --- | --- | --- |
| 1 | SQL 由谁拼 | **dbwm 拼**；slyorm 只产出抽象数据类型（IR） | §3 R1 |
| 2 | 方言归属 | **只在 dbwm**；`util.Dialect` 迁出，`util/` 不再含数据库概念 | §7 |
| 3 | slyorm 要不要方言 | **不要** —— 值成形在 slyorm，语法在 dbwm | §3 |
| 4 | 抽象类型放哪 | 放 `dbwm/`（与方言同包，少一个包） | §4 |
| 5 | 区间怎么表达 | **不设 `range` 算子**：展开成 `gte` + `lt` 放进 `and` 组，括号由组保证 | §4.1 |
| 6 | 分页与总数 | 分页参数**内联整数**；总数由 dbwm 发 `COUNT(*)`（有分组时是分组数） | R4 R5 R7 |
| 7 | 危险写操作 | UPDATE / DELETE **条件为空直接报错** | R4.7 |
| 8 | 行目标契约 | `RowFactory`（`NewRow` + `Targets`），扫描交给驱动，dbwm 校验列数 | R8 |
| 9 | 事务传递 | **ctx 传递**，嵌套复用外层，最外层提交/回滚 | R9.4 |
| 10 | 包名 | `package dbwm`（与目录同名）；现有两个草稿文件已是该包名，内容按本文重写 | §10 |
| 11 | 查询接口命名 | `Qryall`（全量）/ `Qryone`（单条）/ `Qrypgs`（分页） | R7 |
| 12 | 分页参数 | `Page`（页码，从 1 起）+ `Size`（每页条数，0 = 不限）；offset 由 `(Page-1)*Size` 推导 | §4.1 R4.8 |
| 13 | 自增值回写 | dbwm **返回 `Result.IDs`**，由上层反射写回模型字段 —— 不做回调、不做请求对象回写接口 | R6.7 |
| 14 | GROUP 场景 | GROUP BY + 聚合列 + HAVING **一次做齐**；分组查询的总数 = 分组数 | R4.2 R4.3 |
| 15 | IR 类型命名 | 排序项 `Order` → `SortBy`；`Op`/`Cond`/`Comb`/`Node`/`Filter`/`Query`/`Agg` 保留；**分页参数独立成 `Paging`，带分页的查询是 `PagedQuery`（内嵌 `Query` + `Paging`）** | §4.1 |
| 16 | HAVING 引用聚合别名 | IR 只写别名；**渲染时改写成聚合表达式**（PostgreSQL 的 HAVING 不认输出别名，三库通吃） | R4.2 |
| 17 | 方言职责边界 | 算子关键字与 ORDER BY 写法是标准 SQL，**不进方言**；方言只管引用/占位符/分页/取回生成值/参数上限 | R2 |
| 18 | 参数上限 | SQLite 32766、MySQL / PostgreSQL 65535 —— 由方言 `MaxParams` 给出，批量插入据此分块 | R2 R6.5 |
| 19 | `Wnode.Comb` 为空 | 按 `and` 处理（与读规则 `comb=` 默认一致） | §4.3 |
| 20 | 算子名不可覆盖 | `RegisterOperator` 注册已存在的算子名 → 报错（核心算子不可被改写） | R10 |
| 21 | 表名带 schema | `Table` 支持 `schema.table`，按 `.` 分段校验与引用（与 prd-insert 的 `TableName()` 约定一致） | §4.3 R4.1 |
| 22 | 查询返回值 | dbwm 返回**行数**不返回行数据；`RowFactory` 单方法 `NewRow() []any` —— 实现侧创建行对象并收集、返回字段地址，dbwm 把地址交给驱动直接写入；**泛型在 slyorm 层**（泛型工厂包装），业务侧拿 `[]*T` | R7 R8 |
| 23 | 标识符校验 | **删除本地校验**（`util.ValidIdent` 及 dbwm / query / insert 的全部调用点）：方言引用的转义封死注入面，非法 / 不存在的标识符由**数据库报错**暴露（错误带语句文本）；dbwm 因此不再依赖 `util/` | §4.3 R6 |
| 24 | SELECT * | **不支持**：`Columns` 必须非空，为空渲染期报错 —— `SELECT *` 列序由数据库决定，与扫描地址无法对齐 | R4 R8 |
| 25 | 标识符加引号 | 方言只给**引用字符**（`QuoteChar`）；加引号与转义（doubling）统一在 dbwm（`dbwm.QuoteIdent(d, ident)`），渲染器与自定义算子共用一份逻辑 | R2 R10 |
| 26 | 条件值单值化 | `Cond.Args []any` → **`Cond.Arg any`**：一列 + 一算子 + 一个值；in/nin 放**集合**（任意切片类型，渲染时展开成 n 个绑定参数，空集合报错）；条件组类型 `Group` 更名 **`Filter`**（避免与 GROUP BY 混淆） | §4.1 R3 |
| 27 | 排序的表达 | `[]SortBy{Column, Desc}` → **`Sorts []string`**：前缀 `-` 倒序、`+` 或无前缀正序（如 `-id`、`created_at`）—— 一个字符串说清按哪列往哪个方向排 | §4.1 R4 |
| 28 | 取一行怎么执行 | 语句带 `LIMIT 1`，**不带偏移**（取一行不翻页）；入口收 `*Query`（不含分页参数）—— 类型上就分开了，没有页码可填；不改写调用方的查询描述 | R4.8 R7.2 |
| 29 | 占位符编号 | **由渲染器统一分配** —— 算子与写入路径向渲染器要占位符（拿到的是方言形式），编号在整条语句里连续，分配顺序 = 文本里的出现顺序；渲染器单条语句私有，不跨 goroutine 共享 | R5 R10 |
| 30 | 条件树节点形态 | `Node` 从"二选一"联合结构体改为**密封接口**（Composite）：叶子 `*Wcond` 与组 `*Wnode` 实现同一接口 —— 非法联合形态在类型上不可表达，渲染按类型分发，只需校验 nil | §4.1 §4.3 |
| 31 | 条件树类型命名 | `Cond` → **`Wcond`**、`Filter` → **`Wnode`**：`W` 前缀标识 where 族（与 `Qry*` 方法族同构），条件树三类型同根（`Node` / `Wcond` / `Wnode`） | §4.1 |
| 32 | 查询描述与分页的关系 | **`Query`（查什么）与 `Paging`（怎么翻页）分开**，`PagedQuery` 内嵌两者 —— 翻页用 `PagedQuery`（`Qrypgs`），取一行与全量用 `Query`（无分页参数可填，"页码从 1 起"这类约束对不翻页的入口不成立） | §4.1 R7 |
| 33 | 脚本执行 | 增补 **`ExecScript`**：原样 SQL 文本的唯一入口（无参数、ctx 感知事务、空白即 no-op、错误不带正文）——"无逃生舱 / 不做 DDL"的窄修订，服务迁移能力 | R11 §8.10 R9.8 |

---

## 10. 实现参考（文件结构）

> 本节是实现指引，不是需求约束。目标布局沿用既有包的分工原则：**一个包一个能力，按关注点分文件**。

```
dbwm/                          数据库访问中间层（模块根下）
  dbwm.go      包注释、文件分工；句柄 DB（连接 + 方言）与构造入口
  option.go    构造选项：Option / WithDialect（方言）
  ir.go        抽象数据类型：Op / Wcond / Comb / Node / Wnode / Sorts / Agg / Query / Paging / PagedQuery / Insert / Update / Delete，以及写入结果 Result
  dialect.go   方言接口 + ANSI / MySQL / PostgreSQL 实现
  operator.go  算子表、算子注册、值形态校验（in/nin 集合展开）
  sql.go       语句渲染骨架与各语句的渲染入口：SELECT / COUNT / INSERT / UPDATE / DELETE
  sql_select.go SELECT 的子句拼装：列清单 / 分组 / 排序 / 分页
  where.go     条件树 → SQL 片段与参数（括号规则、参数顺序）
  querier.go   查询接口：Qryall / Qryone / Qrypgs
  exec.go      写入接口：Insert / Update / Delete、批量分块、自增值取回
  exec_script.go 脚本执行：ExecScript（原样 SQL 文本的唯一入口）
  deps.go      依赖抽象：Conn / Tx 与 *sql.DB / *sql.Tx 的适配器
  tx.go        事务：开启、提交、回滚、ctx 传递
  detect.go    库型识别（驱动名映射 + version() 探测）
```

**落地时要注意的六件事**：

1. **占位符由渲染器统一分配编号** —— 渲染路径（算子、INSERT / UPDATE 的值）向渲染器要占位符，拿到的就是方言形式，编号在整条语句里连续；分配顺序必须与文本里的出现顺序一致（由渲染顺序保证）；自定义算子的片段仍写 `?`，渲染时换成渲染器分配的占位符，片段的字符串字面量里不要出现 `?`；
2. **参数顺序只有一个来源** —— SQL 文本中占位符从左到右的出现顺序（SELECT 是 WHERE → HAVING，UPDATE 是 SET → WHERE，INSERT 是按行按列）；
3. **错误信息带规则** —— 例如"缺少 WHERE（拒绝全表更新）"、"页码从 1 起"、"第 i 行的值个数与列个数不一致"；标识符错误不带规则（交给数据库报错，但错误里带语句文本）；
4. **方言不做能力探测之外的事** —— 认不出的库型宁可报错由调用方显式指定，不猜；
5. **分组查询的总数走子查询** —— 内层子查询的列清单、分组列、`HAVING` 与外层保持一致（不带 ORDER BY 与分页），否则 `HAVING` 的聚合在内层不可见；
6. **dbwm 不依赖 `util`**（标识符校验已删，见 §4.3.1；失败交给数据库报错）；util 只剩纯文本与取值工具，也不依赖 dbwm —— 无环。
