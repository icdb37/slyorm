# slyorm 业务门面（Collection）需求文档

> 本文定义 **slyorm 层**：业务直接使用的门面 —— 表访问器 `Collection`（单表增删改查、分页列表、事务）。**建表不在范围**：表结构由业务手写 SQL 建好，本层不从模型推导 DDL（见 §1.3）。
>
> 接口**形态**对齐业务接口模板 [example/infra/dbx](../example/infra/dbx/)（现有业务工程的数据库访问封装）：方法名、类型参数、调用形态一一对应，业务 store 层迁移时结构不变；
> **语义**以本项目 PRD 为准 —— 写规则见 [prd-insert.md](prd-insert.md)，读规则见 [prd-select.md](prd-select.md)，SQL 渲染与执行见 [prd-dbwm.md](prd-dbwm.md)，与模板的语义差异与迁移对照见本文 §4。
>
> 分层（见 [prd.md](prd.md)）：`business → slyorm → dbwm → sql.DB`。
>
> 落地包：模块根 `package slyorm`（门面）+ 根下能力包 `model/`、`select/`（新）与 `query/`、`insert/`、`dbwm/`、`util/`（改造；`dml/` 层级取消，见 §7 / §10）。

## 目录

1. [目标与范围](#1-目标与范围)
2. [术语](#2-术语)
3. [分层与职责边界](#3-分层与职责边界)
4. [与 dbx 模板的对照（差异与迁移）](#4-与-dbx-模板的对照差异与迁移)
5. [需求条目 R1–R13](#5-需求条目)
6. [API 一览](#6-api-一览)
7. [与既有包的关系（改造清单）](#7-与既有包的关系改造清单)
8. [已知限制](#8-已知限制)
9. [已拍板](#9-已拍板)
10. [实现参考（文件结构）](#10-实现参考文件结构)

---

## 1. 目标与范围

### 1.1 目标

业务（feature 层的 store / repository）通过 slyorm 访问数据库，形态与 dbx 模板一致：

- **一个表模型 M（struct + `dbx` 标签）声明一张表的可访问面**：列集合与主键从声明推导；表结构（类型、默认值、索引）由业务手写 SQL 建好，本层不推导 DDL；
- `slyorm.NewCollection[M](db, table)` 构造表访问器 `Collection`，之后 `Get / Query / Xget / Xqry / List / Create / Creates / Update / Delete` 覆盖单表全部访问；
- **声明即投影**：任何方法都可以用"表列任意子集的字段组合类型"，不为每张表手写扫描代码；
- 业务代码不出现 SQL 文本、不出现反射、不感知方言。

一句话概括：**业务给"表模型 + 条件结构体"，slyorm 组织 dml 各能力产出 dbwm 的 IR 并执行，业务拿到 `*T` / `[]*T`。**

### 1.2 在范围内

| # | 范围项 |
| --- | --- |
| S1 | 门面句柄 `DB`（构造、事务、关闭）与 `Collection`（表模型绑定、表名、列集合） |
| S2 | 行模型投影解析（T ⊆ M 列集合；计划按类型缓存） |
| S3 | 单行 / 多行查询（`Get` / `Xget` / `Query` / `Xqry`） |
| S4 | 分页列表（`List`：过滤 + 排序 + 总数） |
| S5 | 扫描语义（NULL 兜底、时间兼容、JSON 反序列化） |
| S6 | 插入（`Create`，含自增回填、默认值、JSON 列） |
| S7 | 更新与删除（`Update` / `Delete`，必须带过滤条件） |
| S8 | 事务透传（ctx 传递、嵌套复用，语义继承 dbwm） |
| S9 | 通用数据库类型（分页协议、`Range` 与常用查询类型，R11）与审计列填充（当前操作用户，R12） |

### 1.3 不在范围内

| 不在范围 | 去向 |
| --- | --- |
| JOIN、跨表条件、子查询 | 后续需求 |
| 分组 / 聚合 / HAVING 的门面入口 | 后续需求（dbwm 的 IR 与执行已具备，见 prd-dbwm R4，门面入口用到时再加） |
| Upsert、行级锁、软删、乐观锁 | 后续需求（见 prd-dbwm §1.3） |
| 建表 / DDL / 表结构迁移 | **不做**（不从模型推导 DDL）—— 建表语句（类型、默认值、主键、索引）由业务手写 SQL 编排成脚本目录，经 `db.Migrate` 按水位执行（见 [prd-migration.md](prd-migration.md)）；`type=` `default=` `unique=` `index=` 等建表选项不认（未知选项忽略但保留） |
| 连接池管理、驱动注册 | 调用方（本库不 import 任何驱动，见 prd-dbwm R9.2） |
| 业务协议类型（`QueryPage` 的 json/form/query 绑定标签、`ErrorResponse` 等） | 业务工程自己的包；**通用数据库类型**（分页协议、`Range`、`IDQuery` 一族）由 slyorm 提供（见 R5 / R11），装配时字段拷贝转换 |
| 日志 | 不在门面内做打点 —— 执行路径的 SQL 与错误由调用方按需包 `dbwm`（后续需求另议） |

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| **门面** | 模块根包 `slyorm` —— 业务唯一需要 import 的包，组合 dml 各能力 |
| **表模型 M** | 构造 `Collection` 时绑定的 struct：声明一张表的全部列与主键，是列集合校验与更新保护（Update 剔除主键）的依据 |
| **投影类型 T** | 方法级的字段组合类型：表列的任意子集（含嵌套 `inline` 展开），读取时是结果行类型，写入时是 SET 来源 |
| **条件模型 Q** | 查询过滤的结构体，按**读规则**解析（prd-select） |
| **列集合** | M 按写规则解析出的全部列名 —— 投影、过滤、排序列的合法性都对着它校验 |
| **计划** | 一个投影类型 T 的静态产物：列清单 + 各列抵达字段的路径 + 扫描目标构造方式；按 T 类型缓存 |
| **可空列** | 标了 `null` 或字段为指针的列：写入时 nil / 零值 → SQL `NULL`；列在库里的可空性由建表 SQL 决定 |
| **审计列** | `created_by` / `updated_by` 之类记录操作者的列：标 `deff=curuser`，由门面从 ctx 取当前操作用户填充（见 R12） |

---

## 3. 分层与职责边界

| 关注点 | 归属 | 例子 |
| --- | --- | --- |
| dbx 标签解析（读 / 写两套规则）、列名推导 | `model/`（列名推导的**唯一入口**）、`query/`、`insert/` | `CreatedAt` → `created_at` |
| 表模型绑定、表名、列集合校验、投影校验 | slyorm（`Collection`） | T 里出现 M 没有的列 → 报错 |
| 条件值成形（`like` 补 `%`、区间展开、空切片跳过） | `query/`（改造后，见 §7） | `"张"` → `"%张%"` |
| 写入值成形（零值 / NULL / 默认值 / JSON / 自增跳列） | `insert/`（改造后，见 §7） | 零值 + `null` → `NULL` |
| 扫描目标构造（NULL 兜底、时间兼容、JSON 反序列化） | `select/`（新，包名 `rows`） | NULL → Go 零值 |
| SQL 渲染、执行、事务、方言 | `dbwm/` | `Qryone` / `Insert` / `Tx` |
| 当前操作用户提取（审计列来源） | 业务注入（`slyorm.RegisterUser`，R12） | `csx.Context` → 用户 ID |
| 业务协议（json/form 绑定、Keyword 映射） | 业务工程 | 搜索关键字 → 条件字段 |

**关键切口与 prd-dbwm §3 一致：值成形在 slyorm 侧的能力包，语法在 dbwm。** 门面与能力包不拼 SQL 文本、不出现方言判断；`Collection` 只做"组织"：拿模型元信息组装 `*dbwm.Query` / `*dbwm.Insert` / `*dbwm.Update` / `*dbwm.Delete`，交给 `dbwm.DB` 执行。

**类型的三层归属（目录隔离）**：

| 类型家族 | 归属 | 例子 |
| --- | --- | --- |
| slyorm 与 dbwm **直接共享**的 IR 类型 | `dbwm/`（唯一定义处）—— 门面的签名**直接引用**（`New(conn dbwm.Conn, opts ...dbwm.Option)`、方法内部组装 `*dbwm.Query`），不在根包重复定义、不做转发别名 | `Query` / `PagedQuery` / `Wnode` / `Insert` / `Update` / `Delete` / `Result` / `RowFactory` / `ErrNoRows` |
| 业务侧数据类型 | 门面根包 `slyorm` —— 给业务的协议与常用模型 | `PageQuery` / `ListRequest` / `ListResponse` / `IDQuery` 一族 / `Range`（再导出） |
| 模型元信息类型（静态列集合、主键标记） | `model/` —— insert 与 select 共用 | 列清单、抵达路径、`pk` / `autopk` 标记 |

两个数据类型家族**目录隔离、禁止互混**：业务侧类型不进 `dbwm/`，IR 不进根包 —— 从 import 就能看出一个类型是"跨层共享的"还是"给业务的"。

---

## 4. 与 dbx 模板的对照（差异与迁移）

### 4.1 接口形态对照

| dbx 模板（example/infra/dbx） | slyorm | 说明 |
| --- | --- | --- |
| `DBTX`（注入 `*sql.DB` 或事务） | `dbwm.Conn`（经 `slyorm.New` / `NewSQL` 注入） | 依赖倒置的落点在 dbwm（prd-dbwm R9.1），门面不另设连接抽象 |
| `NewCollection[T](db, table)` | `slyorm.NewCollection[M](db, table)` | 同名同形：表模型 + 表名在构造期绑定，同一模型可映射多张表 |
| `Get[Q, T]` / `Query[Q, T]` / `Xget[T]` / `Xqry[T]` / `List[Q, T]` / `Create[T]` / `Update[Q, T]` / `Delete[Q]` | 同名同形 | 业务 store 层代码迁移时结构不变 |
| `Migrate(ctx, sqls...)`（声明推导建表） | **无对应方法** | 建表由业务手写 SQL 在自己的连接上执行（§1.3）；要自己执行建表 SQL 的，用注入形态 `NewSQL` 并保留 `*sql.DB` 句柄 |
| `cco.ListRequest[Q]` / `cco.ListResponse[T]`、`cco.Range`、`cco.IDQuery` / `IDsQuery` / `CodeQuery` / `StatusRequest` | `slyorm.ListRequest` / `ListResponse`、`slyorm.Range`、`slyorm.IDQuery` 一族（R5 / R11） | 通用数据库类型收进门面；cco 只留 API 协议类型（`QueryPage` 的绑定标签、`ErrorResponse`…），装配时字段拷贝转换（Keyword 由业务映射，不进 slyorm） |
| 事务：把 `*sql.Tx` 当 `DBTX` 传 | `db.Tx(ctx, fn)`，ctx 传递 | 语义更强：嵌套复用、结束后的 ctx 调用报错（prd-dbwm R9.4） |

> `example/` 是**只读参照**（当前缺 `cbo` / `erx` / `upx` 等包与 echo 依赖，不可编译）：本节的对照基准是上表列出的 API 签名（PRD 自述），不依赖 example 可编译。

### 4.2 标签语义对照

dbx 语义**以本项目 PRD 为准**（prd-insert / prd-select / prd-dbwm / 本文）—— `example/` 只是迁移参照，不是规范；模板语义与本项目不一致的地方一律不引入兼容写法，业务侧（example 形态的工程）按下列对照表适配调整：

**查询条件（读规则）**

| 模板写法 | 模板语义 | slyorm 写法 | slyorm 语义 |
| --- | --- | --- | --- |
| `dbx:"col,wf=eq"` 或裸写 `dbx:"col,eq"` | `wf=` 前缀或裸算子段 | `dbx:"col,op=eq"`（可省略，按类型推断） | 统一 `op=`；`wf=` 与裸算子都不认（未知选项忽略但保留，算子回落到类型推断） |
| 标量字段零值 | 一律视为未设置 | 标量加 `omit`，或改指针字段 | 默认精确匹配（零值也参与）；`omit` 才跳过（prd-select R1） |
| `dbx:"name,regex"` | 前缀匹配 `LIKE '值%'` | `dbx:"name,op=like,omit"` | `like` 无 `%` 补成**包含匹配** `'%值%'`（prd-select R4）；要前缀匹配：值自带 `"张%"`，或注册自定义算子（见 §6 注） |
| `dbx:"created_at,wf=range"`（`cco.Range`） | **闭区间** `>= AND <=` | `*slyorm.Range[int]` + `dbx:"created_at"` | **左闭右开** `>= AND <`（prd-select R5）；要闭区间用两个字段 `op=gte` + `op=lte`（列名允许重复） |
| `wf=nrange` | 区间外 `< OR >` | `dbx:",inline,comb=or"` 组内两条 `op=lt` / `op=gt` | 组的括号由结构保证（prd-select R3） |
| 字段间连接 | 恒为 AND | 组：`dbx:",inline,comb=or"` 等 | 支持 and / or / not 嵌套 |

**表模型与写入（写规则；建表选项不认）**

| 模板写法 | 模板语义 | slyorm 写法 | slyorm 语义 |
| --- | --- | --- | --- |
| `dbx:"id,pk,autoinc"` | 自增主键 | `dbx:"id,autopk"` | `autopk` 一个词表达"主键 + 自增"（prd-insert R6） |
| `dbx:"order_id,pk=pk_item"` | 非自增主键（可复合） | `dbx:"order_id,pk"` | `pk` 标记主键列，仅供 Update 剔除 SET（见 R8.3）；组名不认 |
| `dbx:"name,,unique=uk_name"` | 空类型段 + 唯一索引 | `dbx:"name"` | 建表选项（类型段、`unique=` / `index=`）不认（忽略但保留）；唯一约束写进业务的建表 SQL |
| `dbx:"status,int=1"` | SQL 类型 int + DEFAULT 1 | `dbx:"status"` | 类型与 SQL 默认值归建表 SQL；插入时零值照写 `0`（与模板一致）；要 Go 侧零值默认配 `defv=1` |
| `dbx:"created_at,timestamp=now()"` | 类型 + `now()` 默认 | `dbx:"created_at"` | 同上；要"未设置时应用层补当前时间"配 `deff=now` |
| 值字段 | 全写（零值也写） | 同 | 列清单是静态属性（prd-insert R4.1） |
| 零值列不写 | 模板不适用 | `dbx:"col,omit"` | UPDATE 的 SET 阶段零值列不进 `SET`（prd-update R10）；INSERT 不生效（列清单恒定） |
| nil 指针字段 | **跳过该列**，交给 DDL 零值 DEFAULT | **写 SQL `NULL`** | 与读取侧"指针 NULL → nil"闭环；列的可空性由建表 SQL 保证；需要"由数据库填"的列不在模型里声明（`dbx:"-"`） |
| 零值 `time.Time` | 跳过该列交 DEFAULT | 写零值时间；要"未设置交给默认"配 `deff=now` | 默认值取回并写回结构体（prd-insert R5） |
| `json` 列零值 | 跳过该列；空载荷落 `''` | **照常序列化写入**（零值也写 `{"...":"..."}`）；`*T` nil → `NULL` | 读写对称：读侧 NULL / 空串 → 零值（R6） |

**读取扫描**：模板的 `nullZero`（NULL → Go 零值）、`flexTime`（时间多形态兼容）、`flexJSON`（JSON 文本反序列化）在 `select/`（rows）以同语义落地（见 R6）——业务的值类型字段、时间字段、JSON 字段读法不变。

### 4.3 users 业务迁移示例

```go
// 迁移前（example/feature/users/model/bo，dbx 模板规则）
type User struct {
    ID          int          `dbx:"id,pk,autoinc"`
    Name        string       `dbx:"name,,unique=users_unique_name"`
    Status      Status       `dbx:"status,int=1"`
    cbo.Created `dbx:",inline"`
    cbo.Updated `dbx:",inline"`
}

type UserQuery struct {
    ID        []int           `dbx:"id,in"`
    Name      string          `dbx:"name,regex"`
    Status    Status          `dbx:"status,eq"`
    CreatedAt *cco.Range[int] `dbx:"created_at,wf=range"`
}

// 迁移后（slyorm 规则）
type User struct {
    ID          int          `dbx:"id,autopk"`
    Name        string       `dbx:"name"`
    Status      Status       `dbx:"status"`
    cbo.Created `dbx:",inline"`   // 子字段按同样规则迁移；审计列见下
    cbo.Updated `dbx:",inline"`
}
// 建表语句（含 id 自增主键、users_unique_name 唯一约束、status DEFAULT 1）
// 由业务手写 SQL 执行；模型只声明可访问的列与主键标记。

// 审计结构（cbo）：时间由 deff=now 补，操作者由门面从 ctx 取当前用户填充（R12）
type Created struct {
    CreatedAt time.Time `dbx:"created_at,deff=now"`
    CreatedBy int64     `dbx:"created_by,deff=curuser"`
}
type Updated struct {
    UpdatedAt time.Time `dbx:"updated_at,deff=now"`
    UpdatedBy int64     `dbx:"updated_by,deff=curuser"`
}

type UserQuery struct {
    ID        []int            `dbx:"id"`            // 切片推断为 in；空切片 = 无条件
    Name      string           `dbx:"name,op=like,omit"` // 零值跳过；前缀匹配见 §4.2
    Status    Status           `dbx:"status,omit"`       // 省略 op，推断为 eq
    CreatedAt *slyorm.Range[int] `dbx:"created_at"`        // 左闭右开；闭区间用 gte+lte 两字段
}

// store 层：方法调用一一对应
col, err := dbx.NewCollection[bo.User](db, bo.TableUser)
//  →
col, err := slyorm.NewCollection[bo.User](sdb, bo.TableUser)

u.col.Get[cco.IDQuery, bo.User](ctx, qry)   // → col.Get[slyorm.IDQuery, bo.User](ctx, qry)，同形（IDQuery 换用门面类型）
```

> `cco.Range` / `cco.IDQuery` / `IDsQuery` / `CodeQuery` / `StatusRequest` 由门面提供同义类型（`slyorm.Range` 等，见 R11），业务直接换用，不再在 cco 里维护；其余自定义查询 / 更新投影结构体把标签按 §4.2 改写即可继续使用（它们本来就是按列声明的字段组合类型）。

三类**不迁移就静默出错**的高风险写法（迁移检查清单）：

```go
// ① 零值参与反转：DuplQuery.ID 为 0 时模板当"未设置"，本项目默认参与（id <> 0 → 查不到任何行，
//    "按名称查重"静默失效）—— 必须补 omit
type DuplQuery struct {
    ID   int64  `dbx:"id,op=ne,omit"`
    Name string `dbx:"name"`
}

// ② Range 零值下界表达不了：created_at 是 Unix 秒，Min=0 是合法下界，但 Range 的零值边界=未给
//    （prd-select §6 限制 5）—— 改用两个字段，顺带保留模板的闭区间语义
type CreatedQuery struct {
    From *int64 `dbx:"created_at,op=gte"`
    To   *int64 `dbx:"created_at,op=lte"`
}

// ③ regex → 包含匹配：模板 LIKE '值%'（前缀），本项目 like 补 %值%（包含）—— 命中集变大；
//    要保持前缀语义：值自带 "张%"，或注册 prefix 算子（见 §6 / R13.5）
```

---

## 5. 需求条目

每条需求以 **需求 / 规则 / 验收** 三段式给出。

### R1 门面句柄与 Collection 构造

**需求**：业务从一个入口拿到句柄与表访问器，构造期完成全部解析，之后并发安全。

**规则**：

1. `slyorm.DB` 是门面句柄，持有 `*dbwm.DB`；`Open(driver, dsn, opts...)` / `New(conn, opts...)` / `NewSQL(base, opts...)` / `Close()` 原样转发 dbwm（库型识别、方言、`:memory:` 单连接限制等语义见 prd-dbwm R9）；**方言是构造选项**（`dbwm.WithDialect`），不给选项时是 ANSI；
2. `Tx(ctx, fn)` 转发 dbwm（见 R10）；门面不另设连接抽象、不提供"取回底层 `*sql.DB`"的逃生舱；
3. `NewCollection[M](db, table) (*Collection, error)`：M 必须是 struct，可以是指针（按**解指针后的类型**解析与缓存，`*User` 与 `User` 是同一条缓存），否则报错 —— 名字与模板 `dbx.NewCollection[T]` 同名同形（业务迁移零改动），也避免与门面句柄的 `New`（规则 1）重名；
4. **表名显式必填**：支持 `schema.table` 带前缀形式，空串或含非法字符（空白、引用字符）报错 —— 表名与模型解耦，同一模型可映射多张表（与模板一致）；`TableName()` / `RegisterTable` 是 `insert/` 独立使用时的表名机制，`Collection` 不使用；
5. 构造期解析 M，得到：列集合（按写规则）与主键列集合（`pk` 标记的列 + `autopk` 列，Update 剔除 SET 用，见 R8.3）；解析失败（标签写错、列名重复、多个 `autopk` 等）在构造期报错；
6. `Collection` 构造后除计划缓存外只读，**并发安全**；方法内不再解析 M；
7. **泛型只落在构造期**：`Collection` 是非泛型类型 —— M 的信息在构造期固化为 `*model.Model`（列集合 + 主键标记），类型层面不区分不同表的访问器；投影类型 T 由各方法自收，列集合校验与主键剔除一律按运行时列名执行。

**验收**：

- `NewCollection[bo.User](db, "users")` 成功；`NewCollection[bo.User](db, "")` 报错；
- 同一模型 `NewCollection[bo.User](db, "users_bak")` 得到另一张表的访问器；
- M 的标签错误（如 `inline` 与 `json` 并用）在 `NewCollection` 时报错，不在首次查询时。

### R2 行模型投影与计划缓存

**需求**：任何方法可用"表列任意子集的字段组合类型"，错误在首次使用时暴露，成功后按类型缓存；**读侧的列清单是静态属性，不随行值变化**。

**规则**：

1. `model/` 的产物分两层，读写两侧各取所需（同一份字段遍历派生）：
   - **静态列集合**：列名、抵达路径、`json` 标记、`pk` / `autopk` 标记 —— 读侧（投影、过滤、排序白名单）只用它；
   - **值相关写计划**：零值 / NULL 形态、`autopk` 是否参与、默认值 —— 只归写侧（R7 / R8），**不进入读侧**；
2. 投影类型 T 按写规则解析列清单：`inline` 展开（前缀链）、`json` 单列、`col=` / 首段 / snake_case 列名推导，全部沿用 prd-insert R2 / R3 —— `inline` 在读规则里是"条件组"（prd-select R3），两种角色共用同一份字段遍历、产出不同（静态列集合 vs 条件树模板）；
3. **读侧列清单恒含 `autopk` 列** —— "自增列零值不参与"是写侧的值相关规则（prd-insert R6.2），读侧没有"自增列不查"这回事；列清单若随行值变化，缓存计划与扫描地址会错位；
4. T 的每个列必须存在于 M 的列集合，否则报错（报出列名与两个类型名）—— 投影是表列的子集，写错在解析期暴露，不留给数据库；
5. T 未声明任何 dbx 字段 → 报错（避免拼出空列清单的 SELECT）；
6. **计划按 T 类型缓存**（`sync.Map`）：列清单、各列抵达字段的路径、扫描目标构造方式；构建失败不缓存（每次调用都报同一错误）；
7. 计划的列序 = T 的声明序（嵌套按字段位置展开），即 SELECT 列序 = 扫描目标序；
8. 条件模型 Q 按读规则解析（改造后的 `query/`），Q 的过滤列同样校验存在于 M 的列集合；API 契约：`query.ParseWhere(model)` 得条件树模板（按 Q 类型缓存，prd-select R6.5），`tpl.Build(ctx, model)` 取值产出 `*dbwm.Wnode` —— `range` 在此展开为 `gte` + `lt` 的 `and` 组（dbwm 不设 range 算子，prd-dbwm §4.1）。

**验收**：

- `Get[slyorm.IDQuery, bo.UserUpdate]`（T 只含 `name` / `remark`）→ SELECT 只查这些列；
- T 含 `dbx:"id,autopk"` 且该行 `ID == 0` → SELECT 仍含 `id` 列（读侧不跳自增列）；
- T 里写 `dbx:"no_such_col"` → 首次调用报错"列不存在"；
- 同一 T 第二次调用不再走构建路径（缓存命中）。

### R3 单行查询（Get / Xget）

**需求**：按条件取一行，未命中与出错要分得开。

**规则**：

1. `Get[Q, T](ctx, q *Q) (*T, error)`：Q 按读规则解析成条件树，组装 `dbwm.Query{Table, Columns(T 的列), Where}` 交给 `Qryone`（语句带 `LIMIT 1`，prd-dbwm R7.2）；
2. **未命中返回 `(nil, nil)`** —— 门面转译 `dbwm.ErrNoRows`，调用方判 nil（与模板一致；要区分"未命中"与"出错"靠第二个返回值）；
3. 多于一行命中时取第一行，不报错（与模板一致：按唯一键 / 主键取单条是常态用法）；
4. **条件取值后为空 → 报错**（"单行查询缺少过滤条件"）—— 判定口径是**所有条件字段都没有值**（指针 nil、空切片、`sql.Null*` 无效、`Range` 两端皆零，或被 `omit` 跳过）；**零值标量是生效条件**（prd-select R1 精确匹配：`IDQuery{ID: 0}` 产生 `id = 0`，不算空）。空条件查全表取回的是表头行，几乎必然是 bug —— 模板对此只做文档警告，本层直接拦下；
5. `Xget[T](ctx, q any)` 是动态分派版：q 以 `any` 传入，按 q 的**运行时类型**解析 dbx 标签 —— 存在原因是 Go 的类型参数按前缀补齐，指定 T 就必须连带写出可从 q 推断的 Q；语义与 `Get` 一致（含规则 4：不带 dbx 标签的类型解析不出条件 → 报错，不静默退化为查全表）；
6. q 为 nil 指针 → 视为无条件（按规则 4 报错）。

**验收**：

- 按主键查存在的行 → 返回该行；查不存在的行 → `(nil, nil)`；
- 条件字段全部没有值（nil 指针 / 空切片）→ 报错；零值标量（如 `IDQuery{ID: 0}`）是生效条件，不报错；
- `Xget[bo.User](ctx, req)`（req 是请求参数对象，无 dbx 标签）→ 报错（模板此时静默查全表，本层拦截）。

### R4 多行查询（Query / Xqry）

**需求**：按条件取全部命中行，空条件明确等价于全表。

**规则**：

1. `Query[Q, T](ctx, q *Q) ([]*T, error)`：与 Get 同构（条件树 + 投影列），交给 `Qryall`；
2. **空条件 = 查全表**（合法：全表遍历是正当用法），q 为 nil 同样视为无条件；返回空切片（len 0）不是错误（prd-dbwm R6.3）；
3. 排序 / 分页不在 `Query` 上 —— 排序加分页用 `List`（R5）；要"只排序不翻页"的场景，排序进 `List` 且 `Size` 不填（取全量）；
4. `Xqry[T](ctx, q any)` 是动态分派版，语义与 `Query` 一致（空条件查全表）。

**验收**：

- 条件命中 3 行 → 返回 3 个元素；命中 0 行 → 空切片、nil 错误；
- 不带任何生效条件 → 返回全表。

### R5 分页列表（List）

**需求**：一次调用拿"过滤 + 排序 + 总数 + 当页数据"，协议类型归 slyorm。

**规则**：

1. 协议类型（纯数据，不带 json/form 标签）：

   ```go
   // PageQuery 翻页与排序参数。
   type PageQuery struct {
       Page int      // 页码，从 1 起；≤0 按 1 计
       Size int      // 每页条数；≤0 不分页取全量
       Sort []string // 排序项："-col" 倒序、"+col"/"col" 正序（与 dbwm Sorts 同形）
   }

   // ListRequest 分页列表请求：过滤条件 + 翻页参数。
   type ListRequest[Q any] struct {
       PageQuery
       Query *Q // 可为 nil（空条件）
   }

   // ListResponse 分页列表结果。
   type ListResponse[T any] struct {
       Total int64 // 过滤命中的总行数（与排序 / 分页无关）
       Data  []*T  // 当页数据；不分页时是全量
   }
   ```

2. `List[Q, T](ctx, req *ListRequest[Q]) (*ListResponse[T], error)`：req 为 nil 等价零值请求；组装 `dbwm.PagedQuery{Query, Paging}` 交给 `Qrypgs`（总数由 dbwm 另发一条 COUNT，prd-dbwm R7.3）；
3. **分页参数归一化后再交给 dbwm**：`Page ≤ 0` 按 1 计；`Size ≤ 0` 时不分页取全量并**强制 `Page = 1`**（忽略传入的页码）—— dbwm 对 `Size == 0 && Page > 1` 直接报错（prd-dbwm R4.8），前端只传页码是常态，归一化后不会触发；
4. **排序白名单**：`Sort` 每项剥掉方向前缀后的列名必须存在于 M 的列集合，否则报错 —— 排序名直接拼进 SQL，白名单同时挡注入与拼错（与模板一致）；
5. `Total` 是过滤命中的总行数；COUNT 与 SELECT 是两条语句、可能不同快照，要一致把调用包进 `db.Tx`（prd-dbwm R7.5）；
6. 搜索关键字之类的业务参数由业务映射成 Q 的字段，slyorm 不消费（模板的 `Keyword` 同样不进过滤）。

**验收**：

- `Page:2, Size:10`、命中 25 行 → `Total=25`、`Data` 10 行，是第 11–20 行；
- `Sort: ["-created_at"]` → ORDER BY created_at DESC；`Sort: ["-no_such_col"]` → 报错；
- `Size:0` → 不分页，`Data` 是全量。

### R6 扫描语义（RowFactory 与 NULL / 时间 / JSON）

**需求**：dbwm 不认识行模型（prd-dbwm R8），扫描目标的构造与"数据库形态 → Go 形态"的兼容归 slyorm；业务的值字段、时间字段、JSON 字段读法与模板一致。

**规则**：

1. `select/`（rows）提供泛型行工厂：`NewRow` 里 `new(T)`、收集进 `[]*T`、按计划的列序返回各列扫描地址，包装成 `dbwm.RowFactoryFunc` 交给 dbwm —— **计划按 T 类型缓存，工厂与收集切片每次调用新建**（收集切片是本次调用的局部状态，不进缓存）；
2. 扫描目标按字段形态分四类（包装类型收在 `select/`（rows），不散在门面）：

   | 字段形态 | 扫描目标 | NULL 时 | 非空时 |
   | --- | --- | --- | --- |
   | 指针字段 `*V` | 字段地址（database/sql 原生 `**V`） | 置 nil | 按需分配、写入 |
   | `time.Time` / `*time.Time` | 时间兼容目标 | 值保持零值 / 指针置 nil | `time.Time` 直扫；字符串按候选布局解析（`2006-01-02 15:04:05[.fff]`、ISO、日期、紧凑式）；数值按数量级当 Unix 时间戳（秒 / 毫秒 / 微秒 / 纳秒），**按 UTC 解析**（与写侧 `defv` 的 UTC 口径对称；模板的 flexTime 用本地时区，属差异） |
   | `json` 标记字段 | JSON 目标 | 值还原零值 / 指针置 nil | TEXT 列的 JSON 文本 `Unmarshal` 进字段；**空串也还原零值**（与写侧"序列化永远写文本"对称） |
   | 其余基本值类型（含命名类型与 `[]byte`） | NULL 兜底目标 | 写 Go 零值（`""` / `0` / `false` / nil） | 按 driver.Value 契约转换；字符串源可宽松转数值 / 布尔（SQLite 动态类型下 TEXT 列读进数值字段），转不动报错 —— 报错挂 `dbwm.ErrTypeMismatch` 哨兵，业务侧按"类型与值不匹配"判定（见 [prd-error.md](prd-error.md) R5） |

3. **兜底 / 兼容目标自带字段地址**（`nullZero{addr: &row.Age}` 形态，与模板 scan.go 同款）：`Scan` 即写业务字段 —— dbwm 的 `RowFactory` 没有"扫完之后"的钩子（prd-dbwm R8），写回不能依赖扫后回调；
4. 实现 `sql.Scanner` 的字段直扫（自定义类型自己负责形态）；`sql.Null*` 家族同（prd-insert R3 ① 的单列类型）；
5. 列数校验由 dbwm 做（实际列数 = 地址数，prd-dbwm R8.3）；计划的地址数与列清单长度天然一致，错位在构造期就不可能出现；
6. 嵌套 `inline` 路径上的 nil 指针子结构在读前**预分配**（路径保证指向可展开的子 struct）。

**验收**：

- 可空列存 NULL：值字段读出 Go 零值、指针字段读出 nil；
- 时间列以 `2026-01-02 03:04:05` 文本存储 → 读出同一时刻的 `time.Time`；
- JSON 列存 `{"a":1}` → 读出反序列化后的字段值；存 NULL / 空串 → 字段零值；
- 自定义 `sql.Scanner` 类型 → 由实现自己解码。

### R7 插入（Create）

**需求**：一个投影对象插进表，写规则全量生效，自增回填。

**规则**：

1. `Create[T](ctx, p *T) error`：T 按写规则解析（列清单静态、校验 ⊆ M 列集合），值成形沿用 prd-insert 全套：
   - 全列进 INSERT（零值也是有效值）；`null` 零值写 `NULL`；nil 指针写 `NULL`；
   - `autopk` 零值不进列清单，交给数据库生成；显式非零值则写入、不回填；
   - `deff=` / `defv=` 零值时补默认值并**写回结构体**（审计列 `deff=curuser` 见 R12）；
   - `json` 列整体序列化成 JSON 文本（nil 指针写 `NULL`）；
2. 值成形调用 **`insert/` 的 IR 构造入口**（`insert.BuildInsertIR(ctx, v) (*dbwm.Insert, error)`，改造后新增，见 §7）—— 列清单 + 行值 + `AutoPKColumn` 一次产出，门面只填表名并交给 dbwm 执行（值成形一个能力一处实现，门面不自写一份）；自增值从 `Result.IDs` 取回，写回 T 的 `autopk` 字段（复用 `insert/` 的回填规则：只写本来是零值的字段，prd-insert R6）；T 不含 `autopk` 列 → `AutoPKColumn` 留空，不取回自增值；
3. T 可以是 M 本身，也可以是子集投影（只插这些列）；与模板的差异：模板"nil 指针跳列交给 DDL DEFAULT"，本层"nil 指针写 `NULL`"（列的可空性由建表 SQL 保证，读侧 NULL → nil 闭环，见 §4.2）；
4. **批量插入走 `Creates`**：`Creates[T](ctx, ps []T) error` 与 `Create` 同一张表（Collection 构造期绑定的表名）—— `insert.InsertAll` 的表名走 `TableName()` / `RegisterTable` / 类型名机制，与 Collection 的显式表名可能是**两张表**，批量走它会静默写错表；批量语义继承 `insert.InsertAll`：多 VALUES、参数上限自动分块、自增值按行序回填、**分块不自动包事务**（要么包 `db.Tx`，要么接受部分成功，prd-insert R9 / prd-dbwm R6.5）。`insert.InsertAll` 保留，供不经 Collection 的独立使用。

**验收**：

- 插入零值 `autopk` 的对象 → INSERT 不含该列，成功后 `p.ID > 0`；
- 插入 `ID: 42` 的对象 → INSERT 含该列且值为 42，事后 `p.ID == 42`；
- `deff=now` 的时间列 → 插入后 `p.CreatedAt` 非零且与库中一致；
- T 含 M 没有的列 → 首次调用报错。

### R8 更新（Update）

**需求**：按条件更新指定列，全量 SET 语义明确，误操作被拦下。

> UPDATE 的完整语义见 [prd-update.md](prd-update.md)：SET 列判定（R2）、值形态（R3）、默认值（R4）、定位条件（R5）、主键剔除（R6）、"不更新某列"的表达（R7）、执行与错误（R8）、返回值（R9）。本节只记门面的组织职责与对外形态。

**规则**：

1. `Update[Q, T](ctx, q *Q, p *T) error`：q 按读规则解析为条件树；p 按写规则解析（校验 ⊆ M 列集合）；
2. **SET 列 = T 的全部列**（静态清单，零值也是有效值 —— 传零值结构体会把对应列清零，与模板一致；**`omit` 列零值时例外**，不进 `SET`，见 [prd-update.md](prd-update.md) R10）；值成形调用 **`insert/` 的 IR 构造入口**（`insert.BuildUpdateIR(ctx, v) (*dbwm.Update, error)`，改造后新增，见 §7 —— "`deff=` 在 Update 生效"的语义就承载在这个入口），与插入同一套（`null` 零值 → `NULL`、nil 指针 → `NULL`、`json` 序列化），差异是：**`deff=` 生效**（零值时调函数补值并写回结构体 —— 审计列刷新的落点，见 R12；非零值不覆盖）、**`defv=` 不生效**（静态常量没有"更新时刷新"的语义）；
3. **主键列不进 SET** —— 剔除用的主键集合**只认 M**（R1.5 解析所得：`autopk` 列 + `pk` 列，含复合主键）；投影 T 自带的 `pk` 标记不参与剔除判定（否则用投影就能绕过剔除，零值主键清 PK 的静默损坏就回来了）；剔除后 SET 为空 → 报错（"无可更新字段（投影全部为主键列）"）；
4. **条件取值后为空 → 报错**（"更新缺少过滤条件"，判定口径同 R3.4）；dbwm 也会拦（WHERE 为空报错，prd-dbwm R4.7），门面先拦，错误信息带模型与字段；
5. **只改部分列用投影类型**：投影里只声明要改的列（如 `cco.StatusRequest`、`bo.UserUpdate`），与模板的手法一致；
6. 组装 `dbwm.Update{Table, Columns, Values, Where}` 交给 dbwm；返回值只有 error（影响行数不上浮，见已拍板 16）。

**验收**：

- `Update(ctx, idQuery, &UserUpdate{Name: "新名"})` → 只 SET name（及投影里声明的列）；
- p 是含主键列的 M → 主键列不出现在 SET；
- q 的条件字段全零值 → 报错，错误信息指出缺少过滤条件；
- `Update(ctx, idQuery, &StatusRequest{Status: 0})` → status 被清零（零值也是有效值）；
- 更新投影含 `dbx:"updated_by,deff=curuser"` 且值为零 → 补当前操作用户（见 R12）。

### R9 删除（Delete）

**需求**：按条件删除，全表删除被拦下。

**规则**：

1. `Delete[Q](ctx, q *Q) error`：q 按读规则解析，组装 `dbwm.Delete{Table, Where}`；
2. **条件取值后为空 → 报错**（"删除缺少过滤条件"，判定口径同 R3.4），先于 dbwm 拦下；
3. 过滤列校验存在于 M 的列集合（同 R2.6）。

**验收**：条件命中 0 行 → nil 错误（删除是幂等语义）；条件为空 → 报错。

### R10 事务

**需求**：一次业务操作跨多张表 / 多条语句时原子。

**规则**：

1. `db.Tx(ctx, fn)` 转发 dbwm（prd-dbwm R9.4）：fn 收到的 ctx 带事务，**Collection 的全部方法感知**（查询与写入走同一事务）；嵌套调用复用外层，由最外层提交或回滚；fn 出错回滚并返回该错误，fn panic 先回滚再抛回；
2. 事务结束（提交 / 回滚都算）后，用回调里的 ctx 再调 Collection 方法 → 报错，绝不静默改走连接池；
3. 事务只以 ctx 传递：Collection 上不设 `WithTx` / `Begin` 这类句柄形态（不引入第二种事务表达）。

**验收**：

- `db.Tx` 里先 `Create` 再返回错误 → 记录不存在；
- 嵌套两层 `db.Tx` → 只开一个事务；
- 回调返回后用其 ctx 查询 → 报错（prd-dbwm R9.4 验收同款）。

### R11 通用数据库类型

**需求**：业务侧 cco 里的通用数据库类型收进门面统一组织，业务只维护自己的 API 协议类型。

**规则**：

1. 门面提供下列类型（纯数据 + `dbx` 标签按当前规则写好；不占特殊通道 —— 业务可以不用它们、自定义等价模型）：

   ```go
   // Range 是 util.Range[T] 的类型别名（定义只有一份，在 util；门面再导出，不是第二实现）
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

   // StatusRequest 状态字段的部分更新载体（Update 投影）。
   type StatusRequest[T any] struct {
       Status T `dbx:"status"`
   }
   ```

2. 分页协议（`PageQuery` / `ListRequest` / `ListResponse`，R5）与上表同属门面的通用类型；业务的 `QueryPage`（json/form/query 绑定标签）、`ErrorResponse` 等 API 协议留在业务 cco，装配时字段拷贝转换；
3. 主键类型用 `int64`、`ListResponse.Total` 用 `int64`（与自增回填、`dbwm.Result.IDs`、dbwm 的 COUNT 返回对齐）—— 模板 cco 里是 `int`，迁移时注意转换；
4. **适用边界**：`IDQuery` / `IDsQuery` / `CodeQuery` / `StatusRequest` 固定查列 `id` / `code` / `status` —— 表的相应列不叫这些名字时不要用（列不在集合会报错），自定义等价模型即可。

**验收**：

- `IDQuery{ID: 0}` 的 Get → 条件 `id = 0` 生效（不报"缺少过滤条件"），未命中返回 `(nil, nil)`；
- `IDsQuery{IDs: nil}` → 无条件：Query 查全表、Get 报"缺少过滤条件"；
- `slyorm.Range[int]` 与 `util.Range[int]` 是同一类型，可互换使用。

### R12 审计列（当前操作用户）

**需求**：`created_by` / `updated_by` 之类审计列由门面统一填充 —— 从 ctx 取当前操作用户，业务不逐处赋值。

**规则**：

1. `slyorm.RegisterUser(fn func(ctx context.Context) (any, error))`：**装配期注入**"从 ctx 取当前操作用户"的函数 —— slyorm 不认识业务的上下文类型（如 `csx.Context`），断言逻辑由业务注入；
2. 门面把注入的函数注册成默认值函数 **`curuser`**（走默认值注册表，与 `now` 同一张表，prd-insert R5）：模型写 `dbx:"created_by,deff=curuser"` / `dbx:"updated_by,deff=curuser"`。**注册语义**：
   - **重复注册 → panic**（注册表纪律：重名、覆盖内置一律 panic，见 [code.md](../.claude/rules/code.md) §4.3）；
   - **一个函数服务一种返回类型**：`fn` 的返回值必须可赋给目标字段 —— `int64` 用户 ID 与 `string` 账号名是两种字段，后者用 `insert.RegisterDefault("curusername", fn2)` 注册第二个函数名；
   - **函数存在性与类型匹配在取值时校验**（模型解析不查函数表）：未注册或类型不匹配 → Create / Update 报错，错误带字段名与函数名 —— 因此 `RegisterUser` 只需早于第一次写入，不必早于模型解析；
3. **生效时机：Create 与 Update 都生效，且只在字段为零值时**补值并写回结构体 —— 插入填 `created_by`（`updated_by` 若也标了 `deff` 同样填当前用户），更新刷新 `updated_by` / `updated_at`；非零值不覆盖（调用方显式指定优先）；要"每次更新都刷新"的列，更新投影里不要赋值（保持零值），`deff` 才会补；
4. ctx 里的用户从哪来是业务的事：典型做法是 `csx.Context` 携带操作者，`RegisterUser` 的 fn 里做类型断言；`db.Tx` 传递 ctx 不影响断言（`context.WithValue` 不改外层类型）。

   ```go
   // 业务装配期（csx.Context 是业务的上下文类型，slyorm 不依赖它）：
   slyorm.RegisterUser(func(ctx context.Context) (any, error) {
       cs, ok := ctx.(csx.Context)
       if !ok {
           return nil, fmt.Errorf("slyorm: ctx 不是 csx.Context（%T），取不到当前操作用户", ctx)
       }
       return cs.OperatorID(), nil // 返回值类型随字段（int64 / string…）
   })
   ```

**验收**：

- 注入 `RegisterUser` 后 `Create` → `p.CreatedBy` 等于当前用户；`Update`（投影含 `Updated`）→ `p.UpdatedBy` / `p.UpdatedAt` 刷新；
- 更新投影里 `updated_by` 非零 → 不被覆盖；
- 未注入 `RegisterUser` → Create / Update 报错，错误信息提示注册。

### R13 错误、缓存与校验总表

**需求**：写错了能立刻定位；热路径无重复解析。

**规则**：

1. 错误信息以 **`slyorm: `** 开头（门面）；各能力包用各自前缀（`rows: ` / `model: ` / `query: ` / `insert: ` / `dbwm: `）；包装底层错误一律 `%w`；错误要指出**哪个模型 / 哪个列 / 哪条规则**；
2. 解析期（首次构建计划 / 模板）校验清单汇总：

   | # | 规则 | 报错时机 |
   | --- | --- | --- |
   | 1 | M 不是 struct、表名空 / 非法 | `New` |
   | 2 | M / T 的标签写错（沿用 prd-insert R11、prd-select R7 清单） | `New` / 首次用 T |
   | 3 | T 未声明任何 dbx 字段 | 首次用 T |
   | 4 | T / Q / Sort 的列不存在于 M 列集合 | 首次用该类型 / 当次调用 |
   | 5 | Get / Xget / Update / Delete 条件取值后为空（所有字段都没有值或被 `omit` 跳过） | 当次调用 |
   | 6 | Update 剔除主键后 SET 为空 | 当次调用 |
   | 7 | 多个 `autopk` 列（prd-insert R6） | `New` |
   | 8 | `deff=curuser` 而未注入 `RegisterUser`（或返回类型不匹配） | 取值时（Create / Update） |
   | 9 | 投影 T 含多个 `autopk` 列 | 首次用 T |

3. 缓存：M 元信息构造期解析一次；T 计划、Q 模板按类型缓存（`sync.Map`），构建失败不缓存；
4. **注册点沿用各能力包**（门面不另设第二张表）：默认值函数 `insert.RegisterDefault`（门面的 `curuser` 走同一张表）、自定义选项 `RegisterOption`、当前操作用户 `slyorm.RegisterUser`（R12）；自定义算子见规则 5；
5. **自定义算子只在 `query.RegisterOperator` 注册一次**：算子定义为"**语义 + 值成形**"—— 把值补成形、归约为**核心算子组合**（如 `prefix` → `like` + 值后缀 `%`），交给 dbwm 的永远是核心算子，业务不碰 `dbwm.RegisterOperator`；确有无法归约的算子（真·正则）才双注册（`query` 与 `dbwm` 各一次、名字一致）。注册必须发生在对应模型第一次被解析之前（prd-select R8.3、prd-insert R5）；
6. **数据库错误的判定接口在门面**（见 [prd-error.md](prd-error.md) R4）：`slyorm.IsUnique` / `IsTypeMismatch` / `IsNotNull` / `IsForeignKey` / `IsCheck` / `ErrorKind` / `ConstraintOf` —— 业务只 import 门面就能判定错误类别，不必解析驱动错误文本。

**验收**：

- 错误信息能定位到模型与列（如"slyorm: 投影 bo.UserUpdate 的列 no_such_col 不在表 users 的列集合"）；
- 同一个错误类型连续两次调用 → 两次都报错（错误不被缓存）。

---

## 6. API 一览

```go
import (
	"github.com/icdb37/slyorm"
	"github.com/icdb37/slyorm/dbwm" // 注入形态直接用 dbwm 的方言与连接类型（IR 同源于 dbwm）
)

// 句柄（转发 dbwm：方言、库型识别、事务、关闭）
db, err := slyorm.Open("sqlite", "app.db")          // 驱动由调用方注册：_ "modernc.org/sqlite"
db, err = slyorm.New(conn)                          // 注入连接（方言默认 ANSI）
db, err = slyorm.New(conn, dbwm.WithDialect(dbwm.MySQL)) // 注入连接与方言
err = db.Tx(ctx, func(ctx context.Context) error {  // 事务（ctx 传递，嵌套复用）
	return nil
})

// 初始化脚本执行（见 prd-migration）：按水位执行业务 embed 的脚本目录
st, err := db.Migrate(ctx, migrationFS)           // st.Scripts / st.Applied

// 表访问器：表模型 + 表名在构造期绑定（建表/初始化脚本由业务手写 SQL，经 db.Migrate 执行）
users, err := slyorm.NewCollection[bo.User](db, bo.TableUser)

// 查询（Q 条件模型、T 投影类型；X 前缀是动态分派版，q 以 any 传入）
var u *bo.User
u, err = users.Get[slyorm.IDQuery, bo.User](ctx, qry) // 未命中 (nil, nil)；IDQuery 等通用类型由门面提供（R11）
u, err = users.Xget[bo.User](ctx, qry)              // 同上，不用写 Q
var list []*bo.User
list, err = users.Query[bo.UserQuery, bo.User](ctx, q) // 空条件 = 查全表
list, err = users.Xqry[bo.User](ctx, q)

// 分页列表
page, err := users.List[bo.UserQuery, bo.User](ctx, &slyorm.ListRequest[bo.UserQuery]{
	PageQuery: slyorm.PageQuery{Page: 2, Size: 10, Sort: []string{"-created_at"}},
	Query:     &bo.UserQuery{Name: "张"},
})
// page.Total / page.Data

// 写入
err = users.Create(ctx, dat)                        // 自增回填进 dat.ID；默认值写回结构体
err = users.Creates(ctx, people)                  // 批量：与 Create 同一张表；分块不自动包事务（R7.4）
err = users.Update(ctx, qry, &bo.UserUpdate{Name: "新名"}) // 只改投影里声明的列
err = users.Delete(ctx, qry)

// 通用类型（R11）：常用查询类型与 Range（分页协议见上）
q := &slyorm.IDQuery{ID: 7}
r := &slyorm.Range[int]{Min: 0, Max: 100}

// 审计列（R12）：装配期注入"从 ctx 取当前操作用户"，模型用 deff=curuser
slyorm.RegisterUser(fn) // fn: 断言 csx.Context 取操作者，见 R12 示例

// 错误判定（见 prd-error）：业务侧据此处理数据库报错的类别
err = users.Create(ctx, dat)
switch {
case slyorm.IsUnique(err):        // 唯一索引 / 主键冲突 → 提示"已存在"
case slyorm.IsTypeMismatch(err), slyorm.IsNotNull(err): // 值放不进列 → 参数错误
}
slyorm.ErrorKind(err)              // 错误类别（slyorm.Kind）
slyorm.ConstraintOf(err)           // 约束名 / 冲突列（认不出时是空串）
```

- 业务 store 层与模板的调用形态一一对应（见 §4.1 / §4.3）；
- 前缀匹配等扩展算子只在 `query` 注册一次，定义为**语义 + 值成形**（归约为核心算子组合，不是拼 SQL 片段），注册后 `dbx:"name,op=prefix"` 即可用（见 R13.5）：

  ```go
  // prefix → like + 值补 %（归约为核心算子，dbwm 侧永远只认核心算子）
  query.RegisterOperator("prefix", func(col string, v any) (*dbwm.Wcond, error) {
      return &dbwm.Wcond{Column: col, Op: dbwm.OpLike, Arg: fmt.Sprint(v) + "%"}, nil
  })
  ```

- 业务注册能力点直接 import 对应能力包（`query/` / `insert/`），门面不做转发别名；审计列注册 `slyorm.RegisterUser`（R12，装配期一次）。

---

## 7. 与既有包的关系（改造清单）

与 [prd-dbwm.md](prd-dbwm.md) §7 的规划一致并在此落定，另有一条目录决策：**`dml/` 层级取消，能力包全部上移到模块根**（import 路径变为 `github.com/icdb37/slyorm/<能力>`，如 `github.com/icdb37/slyorm/dbwm`；AGENTS §2 的目录约定随实现同步修订）。改造分两轮（先查询后写入），完成后 `util/` 不再含数据库概念：

| 包 | 现状 | 改造后 |
| --- | --- | --- |
| `util/` | `Dialect` + ANSI/MySQL/PostgreSQL + `Detect` + 驱动名映射（与 dbwm **重复**的实现，dbwm 侧已有） | **删除**重复的方言与探测；留标签文本处理、取值与零值判定、`Range[T]` |
| `model/`（新，当前空目录 `dml/model` 上移） | —— | **列名推导的唯一入口**：表 / 列元信息（列名、路径、写规则选项）与主键标记（`pk` / `autopk`）；insert 与 select 共用 —— 投影列集合与写清单从同一份字段遍历派生，不漂移 |
| `query/` | 算子产出 SQL 片段（带引号、占位符），`BuildWhere` 收方言 | 算子改为**语义 + 值成形**，产出 `*dbwm.Wnode`；不再需要方言（prd-dbwm §7 已定）。API：`ParseWhere(model) (*Template, error)`（按类型缓存）+ `tpl.Build(ctx, model) (*dbwm.Wnode, error)`；`range` 展开为 `gte`+`lt` 的 and 组；自定义算子在此注册并归约为核心算子（R13.5） |
| `insert/` | 自带连接抽象与执行；`plan.go` 生成 INSERT 语句；自增值反射写回 | 模型解析与值成形保留；执行改走 dbwm；自增写回改用 `dbwm.Result.IDs`；**新增 IR 构造入口**：`BuildInsertIR(ctx, v) (*dbwm.Insert, error)` 与 `BuildUpdateIR(ctx, v) (*dbwm.Update, error)`（"`deff` 在 Update 生效"的承载点，R7.2 / R8.2）—— 门面复用它，不自写值成形；默认值注册表成为门面 `curuser` 的宿主（R12）；对外 API（`Insert` / `InsertAll` / `BuildInsert` / `Tx`）保留，供独立使用 |
| `select/`（新，包名 `rows`） | —— | 行模型投影解析、计划缓存、RowFactory 与扫描目标构造（NULL 兜底 / 时间兼容 / JSON 反序列化）、组装 `Query` / `PagedQuery` 并泛型执行（返回 `*T` / `[]*T`） |
| 模块根 `slyorm`（新） | —— | 门面：`DB` 转发、`Collection`、`ListRequest` / `ListResponse` |

分层自检口径（AGENTS §7）同步：能力层互不 import（`select/`（rows）只 import `model/` / `dbwm/` / `util/`，不 import `insert/`）；门面（根包）import 全部能力包；错误前缀白名单加入 `slyorm|model|rows`。

**文档同步清单**（目录上移与白名单落地时一并改掉，不留"同步"字样）：AGENTS.md §2（目录约定，含 `select/` 目录 + `rows` 包名的例外，与 `query` 包先例并列）与 §7（自检 grep 的路径、错误前缀白名单 `(insert|query|dbwm|util)` 扩为 `(insert|query|dbwm|util|model|rows|slyorm)`）；`.claude/rules/code.md` 的分层示例；[prd-insert.md](prd-insert.md) §3.2 / R7（`pk` 的 model 层主键标记语义）与文首落地链接；[prd-select.md](prd-select.md) 的落地包 / API 一览 / 文件结构；[prd-dbwm.md](prd-dbwm.md) 的落地包 / §3 归属 / §7 改造表 / import 示例 / §10 文件结构；`util/` 包注释（文件分工表删去已移走的 `dialect.go`，规则包路径去掉 `dml/` 前缀）。

---

## 8. 已知限制

1. **只覆盖单表**：JOIN、跨表条件、用户侧子查询不在范围（prd-dbwm §8 同款）；
2. **门面暂无分组 / 聚合入口**：dbwm 的 IR 与执行已具备（prd-dbwm R4），`Collection` 的聚合查询入口用到时再加；
3. **COUNT 与 SELECT 不在同一快照**：分页的总数与本页数据可能不一致，要一致包事务（prd-dbwm R7.5）；
4. **Get / Qryone 多行时取第一行**：不报错（条件写宽了不会被拦下）；
5. **`like` 的大小写敏感性随库而异**：SQLite / MySQL 默认不区分 ASCII 大小写，PostgreSQL 区分（prd-dbwm §8 同款）；模板的 `regex`（前缀匹配）不进核心算子，按 §4.2 迁移；
6. **`Range` 是左闭右开**：模板的闭区间语义不保留，闭区间用 `op=gte` + `op=lte` 两字段表达；
7. **零值参与语义与模板相反**：模板"空值一律未设置"，本项目"默认精确匹配、`omit` 才跳过"（prd-select R1 已拍板）—— 迁移时给标量条件字段补 `omit` 或改指针；
8. **排序只到列名**：不支持 `NULLS LAST` 等带表达式的排序（prd-dbwm §8 同款）；
9. **自增值只支持整型**（`LastInsertId` 语义，prd-dbwm §8 同款）；MySQL 批量插入的自增回填限制见 prd-insert §7 限制 13（门面单行插入不受影响）；
10. **建表 / DDL 不从模型推导**：建表 / 初始化 / 结构修补语句（类型、默认值、主键、索引）由业务手写 SQL 编排成脚本目录，`db.Migrate(ctx, fsys)` 按水位执行（见 [prd-migration.md](prd-migration.md)，`Open` 打开的库也能用）；用注入形态（`NewSQL` / `New`）且要自持 `*sql.DB` 的照旧自己执行；
11. **不提供原始 SQL 逃生舱**（prd-dbwm §8 同款）：要手写 SQL 由调用方在自己的连接上执行；
12. **Unix 时间戳按 UTC 解析**（R6.2）：模板的 flexTime 按本地时区（`time.Unix`），迁移后同一列读出的时区口径变了 —— 跨时区部署的业务注意；
13. **Get 空条件报错与模板行为不同**（R3.4）：模板静默查全表取表头行，本层直接报错 —— 迁移时依赖旧行为的调用点会暴露为错误（这是有意的：那几乎必然是 bug）。

---

## 9. 已拍板

| # | 议题 | 结论 | 章节 |
| --- | --- | --- | --- |
| 1 | 门面位置 | **模块根 `package slyorm`**（`import "github.com/icdb37/slyorm"`）：业务只 import 根包；能力包在模块根下按关注点分包（`github.com/icdb37/slyorm/<能力>`，`dml/` 层级取消） | §7 §10 |
| 2 | Collection 的泛型参数 | **泛型只落在构造期**：`NewCollection[M](db, table)` 用 M 绑定表模型，返回非泛型的 `*Collection`（类型层面不区分表，列集合与主键以 `*model.Model` 固化）；方法各自收投影类型 T —— 列集合与主键看 M，读写形态看 T | R1 |
| 3 | 表名 | **构造期显式必填**（支持 `schema.table`）；同一模型可映射多张表；不使用 `TableName()` / `RegisterTable`（那是 insert 独立使用的机制） | R1.4 |
| 4 | Get 未命中 | 返回 **`(nil, nil)`**（门面转译 `dbwm.ErrNoRows`），业务判 nil | R3.2 |
| 5 | Get 空条件 | **报错**；判定口径是"**取值后为空**"（所有字段都没有值或被 `omit` 跳过；零值标量是生效条件）；Query / Xqry 空条件 = 查全表 | R3.4 R4.2 |
| 6 | Xget / Xqry | **保留**（动态分派：q 以 `any` 传入，按运行时类型解析；规避 Go 类型参数按前缀补齐的问题） | R3.5 R4.4 |
| 7 | 分页协议类型 | `slyorm.PageQuery` / `ListRequest[Q]` / `ListResponse[T]` **归 slyorm 自有**（纯数据，不 import 业务 cco；Keyword 由业务映射） | R5.1 |
| 8 | 排序白名单 | 排序列必须 ∈ M 列集合（排序名直接拼 SQL，白名单挡注入与拼错） | R5.3 |
| 9 | 扫描语义 | NULL→零值 / nil、时间多形态兼容、JSON 反序列化在 `select/`（rows）落地，**与模板的 nullZero / flexTime / flexJSON 同语义** | R6 |
| 10 | nil 指针写入 | **写 SQL `NULL`**（不跳列）：与读侧"NULL → nil"闭环，列的可空性由业务的建表 SQL 保证；"由数据库填"的列不在模型里声明 | R7.3 §4.2 |
| 11 | 批量插入 | **走 `Collection.Creates`**（与 Create 同一张表 —— `insert.InsertAll` 的表名机制与 Collection 的显式表名可能是两张表，批量走它会静默写错表）；分块不自动包事务等语义继承 `insert.InsertAll`；`InsertAll` 保留供独立使用 | R7.4 |
| 12 | Update 的 SET | **投影类型的全列**（零值也写）；主键列剔除；只改部分列 = 投影类型；`deff` 生效（审计刷新，零值才补）、`defv` 不生效；`omit` 列零值不进 SET（prd-update R10） | R8 R12 |
| 13 | Update / Delete 空条件 | **门面先拦**（错误带模型与字段），dbwm 兜底 | R8.4 R9.2 |
| 14 | 事务形态 | 只以 **ctx 传递**（`db.Tx`），Collection 不设事务句柄形态 | R10 |
| 15 | 建表 | **不做**：`Migrate` / DDL 不实现，建表语句由业务手写 SQL 在自己的连接上执行；`type=` `default=` `unique=` `index=` 等建表选项不认（忽略但保留） | §1.3 §8 |
| 16 | Update 返回值 | 只返回 error，影响行数不上浮（对齐模板；要用影响行数的场景后续再议，不预留） | R8.6 |
| 17 | 主键标记 | 自增主键沿用 **`autopk`**（模板的 `pk,autoinc` 迁移为 `autopk`）；裸 **`pk`** 标记非自增 / 复合主键列，仅供 Update 剔除 SET（主键集合**只认 M**，投影自带的 `pk` 不参与），不渲染任何 DDL；`pk=组名` 的组名不认，迁移改写为裸 `pk`；prd-insert 的选项表随此同步 | §4.2 R8.3 |
| 18 | 错误前缀 | 门面 `slyorm: `，能力包各自前缀；AGENTS 自检白名单同步扩 | R13.1 |
| 19 | 通用数据库类型 | 收进门面：分页协议（R5）+ `Range`（util 的类型别名再导出）+ `IDQuery` / `IDsQuery` / `CodeQuery` / `StatusRequest`；cco 只留 API 协议类型 | R11 |
| 20 | 审计列 | `slyorm.RegisterUser` 装配期注入"从 ctx 取当前操作用户"（业务断言 `csx.Context`），门面注册 **`deff=curuser`**；Create / Update 零值补齐并写回，非零不覆盖；重复注册 panic、返回类型须匹配字段（见 R12.2） | R12 |
| 21 | 目录组织 | **`dml/` 层级取消**：能力包上移模块根（`go.mod` 与 `slyorm.go` 同级）；行模型读取目录 `select/`、包名 **`rows`**（select 是 Go 关键字，不能作包名） | §7 §10 |
| 22 | 类型归属（目录隔离） | slyorm 与 dbwm **直接共享**的 IR 类型唯一定义在 `dbwm/`，门面直接引用、不转发不重定义；业务侧数据类型（`ListRequest` / `IDQuery` 一族 / `Range` 再导出）只进门面根包 —— 两族类型目录隔离、禁止互混 | §3 |
| 23 | 读侧列清单静态 | 读侧列清单**恒含 `autopk` 列**（"自增列零值不参与"是写侧值相关规则）；model 产物分"静态列集合 / 值相关写计划"两层 | R2 |
| 24 | 自定义算子单注册 | 只在 `query.RegisterOperator` 注册一次，定义为语义 + 值成形、**归约为核心算子组合**；不可归约才双注册（query + dbwm，名字一致） | R13.5 |
| 25 | 分页归一化 | `Page ≤0` 按 1 计；`Size ≤0` 不分页并**强制 `Page = 1`**（dbwm 对 `Size==0 && Page>1` 报错，门面归一化后不触发） | R5.3 |

---

## 10. 实现参考（文件结构）

> 本节是实现指引，不是需求约束。目标布局沿用既有包的分工原则：**一个包一个能力，按关注点分文件**。

```
go.mod                        module github.com/icdb37/slyorm（能力包全部在模块根下，dml/ 层级取消）

slyorm.go      包注释与文件分工；DB 句柄（转发 dbwm：Open/New/NewSQL/Tx/Close）
collection.go  Collection：对外 API —— 构造（表模型绑定、列集合、主键标记）与
               Get/Query/Xget/Xqry/List/Create/Creates/Update/Delete
collection_helper.go Collection 的辅助实现：条件构造、投影列校验、读流程、SET 主键剔除
list.go        List 与分页协议类型（PageQuery/ListRequest/ListResponse）
types.go       通用数据库类型（R11）：Range 再导出、IDQuery / IDsQuery / CodeQuery / StatusRequest
audit.go       RegisterUser 与 curuser 默认值注册（R12）
error.go       错误判定接口：类别别名与谓词（prd-error R4）

model/                     表与列的元信息（新，package model）
  model.go       包注释；表模型元信息（列集合、主键、自增列）与解析入口（按类型缓存）
  column.go      列元信息：列名、路径、写规则选项、主键标记（pk / autopk）
  walk.go        字段遍历 —— 列名推导的唯一入口（inline 前缀链、json 单列、单列类型判定）
                 投影列集合与写清单都从它派生

select/                    行模型读取（新；包名 rows —— select 是 Go 关键字，不能作包名）
  select.go      包注释与对外 API：投影解析入口、计划缓存
  plan.go        计划：列清单 + 路径 + 查询组装（dbwm.Query / PagedQuery）
  scan.go        RowFactory 与扫描目标构造（指针 / 时间 / JSON / NULL 兜底四类）

query/  insert/  dbwm/  util/   改造见 §7（由 dml/<能力> 上移到模块根）
```

**落地时要注意的六件事**：

1. **列名推导只有一个入口**（`model/` 的字段遍历）—— 投影校验与写清单从它派生，列集合不漂移（模板 walkFields 的核心理念，结构上继承）；
2. **值成形在能力包、语法在 dbwm** —— 门面与能力包不拼 SQL 文本、不出现方言判断（prd-dbwm §3 的切口贯穿到门面）；
3. **缓存三个东西**：M 元信息（构造期一次）、T 计划、Q 模板（按类型 `sync.Map`）—— 构建失败不缓存错误；`Collection` 构造后并发安全；
4. **扫描目标包装类型收在 `select/`（rows）**（NULL 兜底 / 时间兼容 / JSON 一族），门面不认识扫描细节；
5. **错误前缀随包走**（`slyorm: ` / `rows: ` / `model: `），AGENTS §7 的错误前缀白名单同步扩；
6. **改造分两轮**（先查询后写入，prd-dbwm §7 的节奏），每轮有独立验收线（`go vet ./...` + `go test ./...` 通过、AGENTS §7 自检 grep 干净、新增能力覆盖对应 R 编号）：
   - 第一轮（查询侧）：① 目录上移（`dml/<能力>` → `<能力>/`，import 路径与文档同步清单一并落地）；② `util/` 删除重复方言；③ `model/`（静态列集合 + 主键标记）；④ `query/` 改造（产出 `*dbwm.Wnode`、`range` 展开、模板缓存）；⑤ `select/`（rows：计划缓存 + 自带字段地址的扫描目标）；⑥ 门面查询侧（`DB` / `Collection` 的 Get / Xget / Query / Xqry / List + 分页协议类型）；
   - 第二轮（写入侧）：⑦ `insert/` 改造（`BuildInsertIR` / `BuildUpdateIR`，自增回填走 `Result.IDs`）；⑧ 门面写入侧（Create / Creates / Update / Delete + 通用类型 + `RegisterUser` / `curuser`）；⑨ 跨文档同步（prd-insert 选项表、AGENTS 目录与自检、错误前缀白名单）。
