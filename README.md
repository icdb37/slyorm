# slyorm

> 用**结构体 + `dbx` 标签**声明一张表的读写语义，运行时由反射生成 SQL —— 业务不写手写 SQL、不散落反射调用、不感知方言。

一句话概括：**业务给"表模型 + 条件结构体"，框架把它变成能在各数据库上跑的语句。**

- 模块：`github.com/icdb37/slyorm`
- Go 版本：1.27+
- 状态：开发设计阶段（核心能力已落地，见 [当前进度](#当前进度)）

---

## 目录

- [特性](#特性)
- [快速开始](#快速开始)
- [核心概念](#核心概念)
  - [dbx 标签：写规则](#dbx-标签写规则)
  - [dbx 标签：读规则](#dbx-标签读规则)
  - [列名推导](#列名推导)
- [分层与分包](#分层与分包)
- [用法](#用法)
  - [门面：Collection](#门面collection)
  - [条件模型](#条件模型)
  - [写侧独立使用](#写侧独立使用)
  - [迁移：按水位执行脚本](#迁移按水位执行脚本)
  - [方言](#方言)
  - [事务](#事务)
- [扩展点](#扩展点)
- [当前进度](#当前进度)
- [测试](#测试)
- [文档](#文档)
- [明确不做](#明确不做)

---

## 特性

| 能力 | 说明 |
| --- | --- |
| 声明式读写语义 | 列名、零值策略、NULL、默认值、自增主键、查询条件全部写在 `dbx` 标签里 |
| 零手写 SQL | 业务只给结构体，框架产出 IR 并渲染成 SQL |
| 方言收口 | 标识符引用、占位符、分页、取回自增值、参数上限 —— 全在 `dbwm`，业务代码不动 |
| 双规则集 | 读写两套规则共用一个 `dbx` 标签键，按"这个结构体此刻扮演什么角色"分裂，互不干扰 |
| 扩展点集中 | 自定义选项 / 默认值函数 / 算子 / 表名 / 审计列 —— 都是注册表，重名即 panic |
| 参数化到底 | 值一律走绑定参数，不拼进 SQL 文本；动态标识符必须过列集合白名单 |
| 建表不推导 | 表结构由业务手写 SQL 编排成脚本目录，`migration` 按水位执行并留痕 |

---

## 快速开始

### 1. 引入

```bash
go get github.com/icdb37/slyorm
```

本库**不 import 任何驱动** —— 驱动由调用方注册：

```go
import _ "modernc.org/sqlite" // 或 github.com/go-sql-driver/mysql 等
```

### 2. 定义表模型

```go
package bo

import "time"

type Person struct {
	ID        int64     `dbx:"id,autopk"`       // 自增主键：零值不进 INSERT，成功后回填
	Name      string    `dbx:"name"`             // 零值写 Go 零值
	Age       int       `dbx:"age,null"`         // 零值写 SQL NULL
	Address   Address   `dbx:"addr_,inline"`     // 展开成 addr_city、addr_street
	Tags      []string  `dbx:"tags,json"`        // 整个值序列化成 JSON 存一列
	Remark    string    `dbx:"-"`                // 完全不参与
	CreatedAt time.Time `dbx:"created_at,deff=now"`
}

type Address struct {
	City   string `dbx:"city"`
	Street string `dbx:"street"`
}
```

### 3. 定义条件模型

```go
package bo

type PersonQuery struct {
	ID   int64  `dbx:"id,omit"`            // 零值当没有值
	Name string `dbx:"name,op=like,omit"`  // 包含匹配（自动补 %）
	Age  int    `dbx:"age,omit"`            // 零值当没有值
}
```

> **零值默认参与匹配** —— 不写 `omit` 时，字段是零值也会进 WHERE（`id = 0` 也会查）。
> 想要"没填就别参与"，给字段加 `omit`。

### 4. 装配与使用

```go
package main

import (
	"context"
	"log"

	_ "modernc.org/sqlite"

	"github.com/icdb37/slyorm"
	"yourapp/bo" // 业务自己的模型包（上面两步定义的 package bo）
)

func main() {
	ctx := context.Background()

	db, err := slyorm.Open("sqlite", "./app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 表名在构造期显式传入：同一模型可映射多张表
	people, err := slyorm.NewCollection[bo.Person](db, "person")
	if err != nil {
		log.Fatal(err)
	}

	// 写：自增值回填到 p.ID，默认值写回 p.CreatedAt
	p := &bo.Person{Name: "张三", Age: 0} // Age 零值 → 写 NULL
	if err := people.Create(ctx, p); err != nil {
		log.Fatal(err)
	}

	// 读：未命中返回 (nil, nil)
	got, err := people.Get[bo.PersonQuery, bo.Person](ctx, &bo.PersonQuery{ID: p.ID})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%+v", got)

	// 列表：过滤 + 排序 + 分页
	list, err := people.List[bo.PersonQuery, bo.Person](ctx, &slyorm.ListRequest[bo.PersonQuery]{
		PageQuery: slyorm.PageQuery{Page: 1, Size: 20, Sort: []string{"-id"}},
		Query:     &bo.PersonQuery{Name: "张"},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("total=%d data=%d", list.Total, len(list.Data))
}
```

> 内存库注意：`:memory:` 会被自动限制为单连接；需要多连接共享时用 `file::memory:?cache=shared`。

---

## 核心概念

### dbx 标签：写规则

标签形态：`dbx:"<列名>[,<选项>...]"`，首段是列名，其余是 `key=value` 选项。

| 选项 | 值 | 含义 | 对 INSERT 的影响 |
| --- | --- | --- | --- |
| `col=` | 串 | 显式列名（首段已是列名时二者只能写一个） | 决定列名；嵌套结构内带前缀 |
| `null` | bool | 可空列 | 值为零时写 SQL `NULL`（否则写 Go 零值） |
| `json` | bool | 整对象存一列 | 整个值序列化成 JSON 字符串存成单独一列（不拆字段） |
| `inline` | bool | 展开结构体 | **必须显式写**，否则整组忽略 |
| `autopk` | bool | 自增主键 | 值为零时该列**不参与** INSERT，成功后回填自增值 |
| `deff=` | 函数名 | 默认值**函数** | 零值时调用注册的 Go 函数，取值写回结构体并写入该列 |
| `defv=` | 字面量 | 默认**值** | 零值时写入该常量（按字段类型解析，解析期即校验） |
| `omit` | bool | 零值当成没有值 | **INSERT 不生效**（列清单恒定）；`UPDATE` 的 SET 阶段生效：零值列不进 `SET` |

核心规则：**除了 `dbx:"-"` 的字段与零值的自增列，所有列都参与 INSERT。**

- `pk`（非自增 / 复合主键）与 `auto`（普通自增列）不是写包的概念 —— 出现在标签里按未知选项处理（忽略但保留）。
- `deff` 与 `defv` 语义冲突、`json` 与 `inline` 冲突、`null` 与 `autopk` 冲突 —— **解析期报错**。
- `UPDATE` 时 `deff=` 生效（审计列刷新），`defv=` 不生效。
- `UPDATE` 时 `omit` 生效：值为零的列不进 `SET`，库里原值保持（与读规则同名选项同口径）。

### dbx 标签：读规则

| 选项 | 作用域 | 取值 | 含义 |
| --- | --- | --- | --- |
| `col=` | 叶子 | 串 | 显式列名（读写通用） |
| `op=` | 叶子 | 算子名 | 比较算子；可省略，按字段类型推断 |
| `omit` | 叶子 | bool | **零值当成没有值**：字段为零值时整条条件跳过（默认零值参与匹配） |
| `comb=` | 组 | `and` / `or` / `not` | 组内条件如何连接；默认 `and` |
| `inline` | 组 | bool | **必须写**：结构体字段不写 `inline` 一律忽略 |

- 裸 `and` / `or` / `not` 是 `comb=` 的别名：`dbx:",inline,or"` ≡ `dbx:",inline,comb=or"`。
- 核心算子：`eq` `ne` `gt` `gte` `lt` `lte` `like` `in` `nin` `isnull` `notnull`。
  `op=like` 的值没有 `%` 时自动补成 `%值%`；已带 `%` 原样保留。
- `op=` / `omit` 写在组字段上 → 解析期报错。
- 读规则不认识的选项（写规则专属的、或者完全未知的）**忽略但保留**到 `Opts`，不报错、不阻断 —— DDL 侧、文档生成器还能读到"这里写过什么"。

### 列名推导

列名推导的**唯一入口**在 `model` 包 —— insert 的写清单与 select 的投影列集合都从同一份字段遍历派生，永不漂移。

| 规则 | 说明 |
| --- | --- |
| 列名优先级 | `col=` > 标签首段 > 字段名 snake_case（`CreatedAt` → `created_at`） |
| 不参与的字段 | 未导出字段、未标注 `dbx` 的字段、`dbx:"-"` 的字段 |
| 嵌套结构 | 必须显式 `inline` 才展开；首段是列名前缀（末尾下划线可写可不写），前缀沿嵌套链逐级累加 |
| `json` 字段 | 按单列处理，不带前缀 |
| 列名唯一 | 同一模型内列名不得重复（解析期报错） |
| 表名 | **不能**写在标签里（Go 的 reflect 读不到结构体类型自身的 tag） |

---

## 分层与分包

```
business ─┬─→ slyorm（门面）──→ model / query / insert / rows ──→ dbwm ──→ sql.DB
          ├─→ insert / query（可脱离门面独立用，见下文）──→ dbwm
          └─→ migration ──────────────────────────────────→ dbwm
```

| 包 | 一句话职责 |
| --- | --- |
| `slyorm`（根包） | 业务门面：`Collection` 单表访问、`DB` 句柄、分页协议、通用类型、审计列、`Migrate` 转发 |
| `model` | 列名推导的**唯一入口**：静态列集合、主键标记、抵达路径 |
| `query` | 读规则：条件结构体 → `*dbwm.Wnode`（值成形：like 补 %、区间展开） |
| `insert` | 写规则：模型 → `*dbwm.Insert` / `Update` IR 与执行入口 |
| `rows`（目录 `select/`） | 行模型投影计划、RowFactory、扫描目标（NULL 兜底 / 时间兼容 / JSON） |
| `migration` | 数据迁移：按水位执行业务 SQL 脚本目录并留痕（`sys_migration`） |
| `dbwm` | SQL 渲染与执行、方言、事务（连接接口归它所有） |
| `util` | 共用工具：标签文本处理、取值与零值判定、`Range[T]` |

依赖方向单向向下：门面 → 能力 → 中间层 → 工具层。能力包之间不横向依赖；`model` 不算 —— 它是 insert 与 rows（及门面）共用的列名推导入口，属于被依赖的共享底层。

> 目录叫 `select/`、包名叫 `rows` —— `select` 是 Go 关键字，不能作包名。

---

## 用法

### 门面：Collection

`Collection` 绑定一张表的访问器：表模型 `M` 与表名在**构造期**确定，之后只读、并发安全。

```go
people, err := slyorm.NewCollection[bo.Person](db, "public.person")
```

| 方法 | 说明 |
| --- | --- |
| `Get[Q, T](ctx, q)` | 单行查询；未命中返回 `(nil, nil)`；**条件为空 → 报错**（空条件取回的是表头行，几乎必然是 bug） |
| `Xget[T](ctx, q)` | `Get` 的动态分派版（`q` 以 `any` 传入） |
| `Query[Q, T](ctx, q)` | 多行查询；**条件为空 = 查全表**（合法）；未命中返回空切片 |
| `Xqry[T](ctx, q)` | `Query` 的动态分派版 |
| `List[Q, T](ctx, req)` | 分页列表：过滤来自 `req.Query`，排序与翻页来自 `req` 的分页参数 |
| `Create[T](ctx, p)` | 插入一行；自增值回填、默认值写回结构体 |
| `Creates[T](ctx, ps)` | 批量插入：多 VALUES，超参数上限由 `dbwm` 自动分块（**分块不自动包事务**） |
| `Update[Q, T](ctx, q, p)` | 按条件更新；SET 列 = 投影 `T` 的全部列（零值也写，`omit` 列零值除外），表模型的主键列剔除；**条件为空 → 报错** |
| `Delete[Q](ctx, q)` | 按条件删除；**条件为空 → 报错**（拒绝全表删除） |

投影类型 `T` 是表列的**任意子集**（列集合必须落在表模型 `M` 的列集合里，否则报错），且**必须是非指针结构体**。

分页协议：

```go
type PageQuery struct {
	Page int      // 页码，从 1 起；≤0 按 1 计
	Size int      // 每页条数；≤0 不分页取全量
	Sort []string // "-col" 倒序、"+col"/"col" 正序；列名必须在表模型的列集合里
}

type ListRequest[Q any] struct {
	PageQuery
	Query *Q // 可为 nil（空条件，查全表）
}

type ListResponse[T any] struct {
	Total int64
	Data  []*T
}
```

### 条件模型

条件模型就是"带 `dbx` 标签的普通结构体" —— 框架按标签把它翻译成条件树：

```go
type PersonQuery struct {
	ID        int64                  `dbx:"id,omit"`             // 精确匹配
	Name      string                 `dbx:"name,op=like,omit"`   // 包含匹配
	Age       int                    `dbx:"age,omit"`            // 零值跳过
	CreatedAt *util.Range[time.Time] `dbx:"created_at"`          // 左闭右开区间 → gte + lt 组
	Any       struct {               // 条件组
		City string `dbx:"city"`
		Tel  string `dbx:"tel"`
	} `dbx:",inline,comb=or"`
}
```

- `Range[T]` 是左闭右开区间 `[Min, Max)`；某一端是零值就当"没给这一端"，两端都零值时整条条件不参与。
- `Range` 是读侧概念：在读规则里展开成一个**组**（`gte` + `lt`，整体加括号，不会被 or 组拆散）；在写规则里它就是普通结构体字段，不带 `inline` / `json` 不参与写入。
- 条件模型可直接用 `query` 包独立使用：

```go
tpl, err := query.ParseWhere(&q)      // 解析期校验：算子存在性、算子与类型匹配、组合方式合法
w, err := tpl.Build(ctx, &q)          // *dbwm.Wnode；没有生效条件时返回 nil
```

### 写侧独立使用

`insert` 可脱离门面单独用（自带 `DB` 句柄与表名推导机制）：

```go
db, err := insert.Open("sqlite", "./app.db")

p := &bo.Person{Name: "张三"}
res, err := db.Insert(ctx, p)             // 表名按 TableName() > RegisterTable > 类型名 snake_case
err = db.InsertAll(ctx, people)            // 批量
stmt, args, err := db.BuildInsert(ctx, p)  // 只生成 SQL 不落库
```

表名推导优先级：`TableName()` 方法 > `RegisterTable` 登记 > 类型名 snake_case：

```go
insert.RegisterTable[bo.Person]("public.person")
```

门面的 `Collection` 走构造期显式表名，所以同一模型可映射多张表。

### 迁移：按水位执行脚本

建表 / 初始化 / 结构修补由**业务手写 SQL**，编排成脚本目录交给 `migration` 按水位执行：

```go
//go:embed v1.0/*.sql
var scripts embed.FS

stats, err := db.Migrate(ctx, scripts)
// stats.Scripts 装载数 / stats.Applied 本次执行数
```

- 脚本文件名形如 `{版本号}_{序号}_{名称}.sql`（如 `1000_001_seed_admin.sql`），按（版本号, 序号）升序执行。
- 执行记录落在 `sys_migration` 表，已执行过的不重复执行。
- 任一脚本失败即停止并返回错误（失败即停是失败可重试的前提），失败脚本在事务外留 `success=0` 的记录。
- 空目录零副作用：不建表、不发任何 SQL。

### 方言

核心 SQL 是**标准 SQL**；方言只管标准 SQL 覆盖不到的少数几点：标识符引用、占位符、分页写法、取回自增值的方式、一条语句的参数上限。

| 方言 | 标识符引用 | 占位符 | 取回自增值 |
| --- | --- | --- | --- |
| `dbwm.ANSI`（SQLite） | `"列"` | `?` | `LastInsertId` |
| `dbwm.MySQL` | `` `列` `` | `?` | `LastInsertId` |
| `dbwm.PostgreSQL` | `"列"` | `$1,$2,…` | `RETURNING "列"` |

方言是**构造选项**；`Open` 不给选项时按驱动名映射（`sqlite`/`sqlite3`、`mysql`/`mariadb`、`postgres`/`pgx`），映射不到就连库探测（`version()` / `sqlite_version()`），**认不出来直接报错，不猜**。`New` / `NewSQL` 不做识别，不给选项时直接用 ANSI：

```go
db, err := slyorm.Open("sqlite", "./app.db")                        // 自动选方言
db, err := slyorm.Open("custom", dsn, dbwm.WithDialect(myDialect)) // 显式指定
```

新增数据库支持 = 新增一个方言实现，**业务代码不动**。

### 事务

事务用 ctx 传递 —— `fn` 收到的 ctx 自动带上事务，嵌套调用复用外层事务，由最外层提交或回滚：

```go
err := db.Tx(ctx, func(ctx context.Context) error {
	if err := people.Create(ctx, p); err != nil {
		return err
	}
	return people.Update[bo.PersonQuery, bo.Person](ctx, q, &up)
})
```

`Collection` 的全部方法感知事务。

---

## 扩展点

扩展点集中在**注册表**里，注册必须在对应模型**第一次被解析之前**完成（模型元信息按类型缓存，之后再注册对已缓存的模型无效）。破坏不变量的调用一律 `panic`。

| 注册函数 | 归属 | 用途 |
| --- | --- | --- |
| `insert.RegisterOption(key, handler)` | `insert` | 自定义写侧选项 |
| `query.RegisterOption(key, handler)` | `query` | 自定义读侧选项 |
| `insert.RegisterDefault(name, fn)` | `insert` | 默认值函数，供 `deff=<名字>` 使用（内置 `now`） |
| `query.RegisterOperator(name, fn)` | `query` | 自定义算子（归约为核心算子组合，不拼 SQL 片段） |
| `insert.RegisterTable[T](name)` | `insert` | 表名登记 |
| `slyorm.RegisterUser(fn)` | `slyorm` | 审计列的当前操作用户（注册成默认值函数 `curuser`） |

```go
// 自定义算子：语义 + 值成形，产出核心算子叶子
query.RegisterOperator("prefix", func(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpLike, Arg: fmt.Sprint(v) + "%"}, nil
})

// 审计列：模型写 dbx:"created_by,deff=curuser"，取值函数由业务注入
slyorm.RegisterUser(func(ctx context.Context) (any, error) {
	return currentUserID(ctx), nil
})
```

一个默认值函数服务一种返回类型：`int64` 用户 ID 用 `curuser`，`string` 账号名另注册一个函数名（如 `curusername`）。

---

## 当前进度

| 能力 | 状态 | 落地包 |
| --- | --- | --- |
| SQL 渲染与执行、方言、事务 | 已落地 | `dbwm/` |
| 列名推导与模型元信息 | 已落地 | `model/` |
| 读规则（条件树） | 已落地 | `query/` |
| 写规则（写入 IR） | 已落地 | `insert/` |
| 行模型投影与扫描 | 已落地 | `select/`（包名 `rows`） |
| 数据迁移（水位执行 + 留痕） | 已落地 | `migration/` |
| 业务门面（Collection / 分页 / 审计 / Migrate） | 已落地 | 根包 `slyorm` |
| 分组聚合的门面入口 | 未落地 | `dbwm` 的 IR 已具备（`GroupBy` / `Aggs` / `Having`），用到时再加 |
| JOIN 的门面入口 | 未落地 | `dbwm` 的 IR 尚未设计，需要时先补 PRD |
| PG / MySQL 真库验证 | 未做 | 当前验证以内存 SQLite + 替身连接为主 |

---

## 测试

```bash
go vet ./...
go test ./...
```

测试是**同包白盒**（`package slyorm` / `model` / `rows` / `query` / `insert` / `migration` / `dbwm` / `util`），可以直接测未导出的解析函数：

- 要验证真实 SQL 行为 → 内存 SQLite + 手写建表（自持 `*sql.DB` 建 DDL，`:memory:` 须 `SetMaxOpenConns(1)`）；
- 要断言"执行了哪条 SQL" → 用 `dbwm` 测试里的 `fakeConn` 替身，不碰真实数据库。

---

## 文档

需求与规则以 PRD 为准（**文档先行**：先改 PRD，再改代码，最后补测试）。PRD 里编号的需求条目（R1、R2…）是实现的依据。

| 文档 | 覆盖范围 |
| --- | --- |
| [docs/prd.md](docs/prd.md) | 框架总纲：定位、分层、分包职责、跨包硬约束 |
| [docs/prd-dbwm.md](docs/prd-dbwm.md) | IR、方言、渲染、执行、扫描、事务 |
| [docs/prd-insert.md](docs/prd-insert.md) | 写规则：写侧选项、零值 / NULL / 默认值 / 自增 |
| [docs/prd-select.md](docs/prd-select.md) | 读规则：读侧选项、条件树、算子 |
| [docs/prd-slyorm.md](docs/prd-slyorm.md) | 业务门面：Collection、扫描语义、分页协议、审计列 |
| [docs/prd-migration.md](docs/prd-migration.md) | 数据迁移：脚本装载、水位、留痕 |

项目开发规范在 [.claude/CLAUDE.md](.claude/CLAUDE.md)：分层与依赖、编码纪律、注释规范、测试约定、提交前自检。

---

## 明确不做

- 不写兼容层、弃用别名、双路径实现 —— 旧设计可以直接改掉；
- 不引入第三方 ORM / 查询构建库 —— 语义由本项目自己的 PRD 定义；
- **不从模型生成 DDL、不做表结构推导** —— 建表 / 初始化 / 结构修补由业务手写 SQL 编排成脚本目录，`migration` 按水位执行；
- 不把驱动、连接池、事务实现泄漏进业务逻辑；
- 不提供"取回底层 `*sql.DB`"的逃生舱 —— 注入 `*sql.DB` 的一方自己留着句柄即可；
- 不在框架内打日志 —— 执行统计以返回值交给业务（迁移返回 `Stats`），日志由业务记；
- 新能力先写 PRD，再写实现。
