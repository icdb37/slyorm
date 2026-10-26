# 数据插入（INSERT）需求文档

> 本文只覆盖一件事：**把一个模型对象写进数据库**。
> 写侧选项总表见 [§3.2](#32-选项总表与-insert-相关)；读侧（查询条件）的选项与语义见 [prd-select.md](prd-select.md)，
> 落地实现见 [`insert/`](../insert/)（包 `github.com/icdb37/slyorm/insert`，产出 `*dbwm.Insert` / `*dbwm.Update` IR），
> 已适配 SQLite / MySQL / PostgreSQL（方言差异见 [§3.3](#33-数据库方言)，方言在 [prd-dbwm.md](prd-dbwm.md)）。

## 目录

1. [目标与范围](#1-目标与范围)
2. [术语](#2-术语)
3. [`dbx` 标签语法](#3-dbx-标签语法insert-子集)
4. [需求条目 R1–R11](#4-需求条目)
5. [字段 → 是否参与 INSERT 对照表](#5-字段--是否参与-insert-对照表)
6. [API 一览](#6-api-一览)
7. [已知限制与待拍板](#7-已知限制与待拍板)

---

## 1. 目标与范围

### 1.1 目标

用**一个结构体 + `dbx` 标签**描述一张表的写入行为，由 ORM 反射生成 `INSERT` 语句并执行：

- 列名、零值策略、默认值、自增主键等写入语义**声明式**表达，不写手写 SQL；
- 嵌套结构**显式声明 `inline`** 后展开成多列，并支持**列名前缀**隔离；也可以声明 `json` 把整个对象存成**一列 JSON**；
- 默认值取自 Go 函数（`deff=`）或字面量（`defv=`），取值**写回结构体**，插入后立刻可读；
- 自增主键插入成功后**回填**到结构体字段；给了显式值时按显式值写入；
- 多表写入能落在**同一个事务**里。

### 1.2 在范围内

| # | 范围项 |
| --- | --- |
| S1 | `INSERT` 语句的生成（列清单、占位符、参数绑定） |
| S2 | `INSERT` 的执行（单条、批量多 VALUES） |
| S3 | `dbx` 标签中**与写入相关**的选项 |
| S4 | 嵌套结构展开（需显式 `inline`）+ 列名前缀规则；或整对象存成一列（`json`） |
| S5 | 默认值（函数 / 字面量）、自增主键回填、零值与 NULL 的落库形态 |
| S6 | 事务（跨表原子写入） |

### 1.3 不在范围内（另有 PRD）

| 不在范围 | 涉及内容 | 去向 |
| --- | --- | --- |
| 建表 / DDL | `type=` `null=` `unique=` `index=` `fk=` `default=` | 建表 PRD |
| 查询 | `op=` `comb=` `sort=` `agg=` 以及读规则、条件分组 | 查询 PRD |
| 更新 / 删除 | `UPDATE` 的列判定、`DELETE` 的定位 | 更新见 [prd-update.md](prd-update.md)；删除见 [prd-slyorm.md](prd-slyorm.md) R9 |

> 这些选项在本 PRD 中不做语义规定：标签里可以被解析（当前实现会解析并原样保留到列的 `Opts`），但**不参与 INSERT 的任何行为**。
> 写包里**不认得**的选项统一按"未知选项"处理：**忽略但保留到 `Opts`，不报错** ——
> 读规则专属的选项（`op=` `comb=` `sort=` `agg=` 等）由查询包解释，
> 已移除的选项（`omitempty` `force` 等）本就没有语义，两者都不该阻断写入。
> `omit` 是个例外：写包**认它**（取值按布尔校验），但它**只在 UPDATE 的 SET 阶段生效**（零值列不进 `SET`，见 [prd-update.md](prd-update.md) R10）—— INSERT 的列清单恒定，它不参与 INSERT 的任何行为。
> 反过来，写入专属选项（`autopk` `deff` `defv`）出现在查询模型里属于误用 —— 由查询包按它自己的规则处理，本 PRD 不做规定。
> `pk` 与 `auto` **不是本包的选项**：主键组只在 UPDATE/DELETE 里用于定位，普通自增列在本包没有意义 —— 自增主键一律写 `autopk`。它们出现在标签里按未知选项处理（忽略但保留到 `Opts`，不报错）。

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| **模型** | 用于写入的结构体，按**写规则**解析 `dbx` 标签 |
| **列** | 模型的一个可写字段，对应数据库的一列 |
| **嵌套结构** | 字段类型是结构体且不是单列类型（见 R3 ①），**显式声明 `inline`** 后展开成多列 |
| **JSON 列** | 标了 `json` 的字段：不拆列，整个值序列化成 JSON 字符串存成一列（见 R3 ⑨） |
| **列名前缀** | 嵌套结构展开时加在子列名前面的前缀，如 `addr_` |
| **零值** | 字段的 Go 零值（`0` `""` `nil`）；`time.Time` 用 `IsZero()` 判定，`0001-01-01` 算零值（见 R4） |
| **NULL 列** | 标了 `null` 的列：值为零时写 SQL `NULL`，否则写 Go 零值（见 R4） |
| **显式值** | 自增列上由调用方赋好的非零值 —— 按它写入数据库，不走自增（见 R6） |
| **回填** | 把数据库生成的自增值写回结构体字段。**只有 `autopk` 列会回填**，其它主键由调用方自己指定（见 R6） |

---

## 3. `dbx` 标签语法（INSERT 子集）

### 3.1 语法与解析规则

```
dbx:"<列名>[,<选项>...]"
选项 := 标识符 | 标识符 "=" 值
```

1. **列名**：第一段不含 `=`、且不是已知选项名的内容就是列名；没写就用字段名的 snake_case（`CreatedAt` → `created_at`，`ID` → `id`）。
2. **忽略字段**：`dbx:"-"` 表示该字段完全不参与。
3. **选项形式**：统一 `key=value`；裸 flag（`pk`）等价于 `key=true`。布尔取值接受 `true/false/1/0/yes/no/on/off`，空串为真。
   —— 选项名**统一转小写**（`dbx:"id,NULL"` 与 `dbx:"id,null"` 是一回事）；选项值**按给定处理**：大小写原样保留，**外层引号脱掉**（成对的两个引号表示一个引号），夹在中间的引号不动 —— 这一步对所有选项一视同仁，DDL 侧要自己补引号。
4. **切分**：按逗号切分，**括号与引号内的逗号不切分**（`type=decimal(10,2)` 是一个整体）；切分出的**每一段都先去掉两端的空白字符**（`" id , null "` → 列名 `id` + 选项 `null`）。
5. **引号**：只有**成对出现**时才是分组标记（`defv='a,b'` 是一个整体）；落单的引号按普通字符处理（`defv=it's,null` 仍切成两段）。引号的具体语义见 [R5](#r5-默认值deff-默认值函数--defv-默认值)。
6. **参与与否**：**未标注 `dbx` 的字段整个被忽略** —— 不产生列，列名也不会被推导；非导出字段自动忽略。
   —— 标了 `dbx` 的**结构体字段**还要显式写 `inline` 才会被解析，否则整个字段忽略（见 R3 ②）；
   —— 标了 `json` 的结构体字段按**单列**处理，不展开（见 R3 ⑨）。
7. **嵌套结构字段的第一段是列名前缀，不是列名**（见 R3 ③）。
8. **重复选项**视为笔误，解析期报错（`dbx:"id,pk,pk"`）。
9. **语义冲突的选项组合**（`deff`+`defv`、`json`+`inline`、`null`+`autopk`）解析期报错，见 [R11](#r11-错误处理与可诊断性)。
10. **跨规则误用**：写入专属选项出现在查询模型里 → 由查询包报错并提示属于哪套规则（本包不认这些选项，也不报错）。
11. 表名**不能**写在 `dbx` 标签里（Go 的 reflect 读不到结构体类型自身的 tag），见 [R1](#r1-表名与模型定位)。

### 3.2 选项总表（与 INSERT 相关）

| 选项 | 值 | 含义 | 对 INSERT 的影响 |
| --- | --- | --- | --- |
| `col=` | 串 | 显式列名 | 决定该列在 INSERT 里的列名；嵌套结构内同样带前缀 |
| `null` | bool | 可空列 | 值为零时写 SQL `NULL`（否则写 Go 零值）；DDL 侧据此生成可空列 |
| `json` | bool | 整对象存一列 | 整个值序列化成 JSON 字符串，存成单独一列（**不拆字段**） |
| `inline` | bool | 展开结构体 | 结构体字段必须显式声明才会被解析，否则整组忽略 |
| `autopk` | bool | 自增主键 | 值为零时该列**不参与** INSERT，成功后回填自增值 |
| `deff=` | 函数名 | 默认值**函数** | 零值时调用注册的 Go 函数，取值写回结构体并写入该列 |
| `defv=` | 字面量 | 默认**值** | 零值时写入该常量（按字段类型解析，解析期即校验） |
| `omit` | bool | 零值当成没有值 | **INSERT 不生效**（列清单恒定，见 R4.1）；UPDATE 的 SET 阶段生效：零值列不进 `SET`（[prd-update.md](prd-update.md) R10） |

> `col=` `json` `inline` 读写通用；`null` `autopk` `deff` `defv` 是**写规则专属**，在查询模型里出现会报错。
> `default=` 是 DDL 侧的 SQL 默认值，与 `defv=`（常量默认值）不是一回事。
> `force` **已移除**：本包不认它（按未知选项处理：忽略但保留到 `Opts`）。自增列要写显式值，把值直接赋给字段即可（见 R6）；UPDATE 要写零值，用指针字段表达（见本文 §6 API 一览）。
> `omit` **在 INSERT 里不生效**：列清单恒定（见 R4.1），零值照写；它在 UPDATE 的 SET 阶段生效（零值列不进 `SET`，见 [prd-update.md](prd-update.md) R10）。不参与写入用 `dbx:"-"`，零值写 `NULL` 用 `null`（见 R4）。
>
> **`pk` 与 `auto` 不在本表里 —— 本包只支持 `autopk`**：`pk`（主键组）只用来在 UPDATE/DELETE 里定位记录，
> `auto`（普通自增列）在本包没有意义。两者出现在标签里都被忽略（保留到 `Opts`），不报错。
> 需要"自增主键"时一律写 `autopk`；非 `autopk` 的主键 ID **由调用方自己指定**（见 R6）。

### 3.3 数据库方言

核心语句是**标准 SQL**：

```
INSERT INTO "<表>"("<列1>","<列2>",...) VALUES(?,?,...)
```

方言只负责标准 SQL 覆盖不到的少数几点：

| 差异点 | SQLite | MySQL | PostgreSQL |
| --- | --- | --- | --- |
| 标识符引用 | `"列"` | `` `列` `` | `"列"` |
| 占位符 | `?` | `?` | `$1,$2,…` |
| 取回自增值 | `LastInsertId`（批量按末行推算） | `LastInsertId`（单条可靠；批量报错，见 §7 限制 13） | `RETURNING "列"`（按行取回，不靠推算） |

- **业务规则与数据库无关** —— 列清单、零值语义、默认值、自增回填对所有数据库一样（见 R8.9）；
  唯一的例外是**批量插入的自增回填**：能不能从 `LastInsertId` 推出一批值由方言决定，推不准就报错，不瞎猜。
- 库型识别**先按驱动名映射**（`sqlite`/`sqlite3`、`mysql`/`mariadb`、`postgres`/`pgx`），映射不到再连库探测（见 R8.10）；
  探测认不出的版本串直接报错，不猜。
- 新增数据库支持 = 新增一个方言实现，业务代码不动。

---

## 4. 需求条目

每条需求以 **需求 / 规则 / 验收** 三段式给出；验收点即该条需求的完成标准。

### 4.1 模型与列

#### R1 表名与模型定位

**需求**：ORM 必须能确定写入哪张表。

**规则**（优先级从高到低）：

1. 模型实现 `TableName() string` 方法 → 用它的返回值；返回值可以是 `"public.person"` 这样的**带前缀形式**，ORM 拆成 schema + 表名，生成 `"public"."person"`；
2. `dml.RegisterTable[Person]("person")` 显式登记；
3. 类型名的 snake_case（`Person` → `person`）。

表名与前缀每一段都必须是合法标识符（`[A-Za-z_][A-Za-z0-9_]*`），否则解析期报错。

**验收**：`func (p *Person) TableName() string { return "person" }` → 生成 `INSERT INTO "person"(...)`。

#### R2 列名推导

**需求**：每个可写字段都要有一个确定的列名。

**规则**：

1. `col=<值>` 显式指定；
2. 否则取 `dbx` 标签第一段；
3. 否则取字段名的 snake_case；
4. 嵌套结构内的子字段，列名 = **所在嵌套链的前缀** + 上述结果（见 R3 ③）；
5. 同一张表内列名不得重复，重复在解析期报错。

**验收**：`Age int` → 列名 `age`；`Age int \`dbx:"col=years"\`` → 列名 `years`。

### 4.2 嵌套结构

#### R3 嵌套结构展开与列名前缀

**需求**：模型里的嵌套结构（子对象）要能展开成同一张表的多个列，并且列名可加前缀避免撞车。

**规则**

**① 哪些字段算嵌套结构**

字段类型是结构体，且**不是**单列类型时，该字段就是嵌套结构。命名结构体字段与匿名内嵌结构字段同等对待。

单列类型（不展开）：实现了 `driver.Valuer` 或 `sql.Scanner` 的类型，以及这些内置类型：

- `time.Time`
- `sql.NullString` / `NullInt64` / `NullInt32` / `NullFloat64` / `NullBool` / `NullTime` / `NullByte`
- `Range[T]`（区间类型，属于查询侧）

> 想把整个结构体存成**一列 JSON**？两条路：给字段加 `json` 选项（见 ⑨），或给该类型实现 `driver.Valuer`（或 `sql.Scanner`）—— 后者会被当成单列处理，不展开、不需要 `inline`。

**② 必须显式声明 `inline`**

**结构体字段默认不参与写入**，只有显式写了 `inline` 才会被解析：

```go
type Person struct {
	ID      int64   `dbx:"id,autopk"`
	Address Address `dbx:"addr_,inline"`  // 展开：addr_city、addr_street
	Extra   Extra   `dbx:"-"`              // 忽略
	Remark  Remark                        // 没写 dbx → 忽略
}
```

- 没写 `inline` 的结构体字段**整个被忽略**，不产生任何列；
- `inline` 可以写成裸 flag，也可以写 `inline=false` 显式关闭；
- 嵌套结构上写规则**只认 `inline`**：其它选项（`null` `deff=` 等）与重复选项都在解析期报错 ——
  单列语义请写在子字段上；
- 这条规则**读写两套规则统一生效**（读规则下 `inline` 表示"这个结构体要解析成条件组"）；
- 好处：模型里放一个辅助结构体（审计信息、临时计算结果）不会被误展开，也不会意外撞列名。

**③ 前缀规则**

嵌套结构字段的 `dbx` 标签**第一段是列名前缀，不是列名**：

```
dbx:"<前缀>,inline[,选项...]"
```

- 前缀以 `"{prefix}_"` 的形式拼在**每个子字段列名**前面；
- **前缀末尾的下划线可写可不写**：`dbx:"addr,inline"` 与 `dbx:"addr_,inline"` 等价，最终列名都是 `addr_city`、`addr_street`；
- 前缀可以为空：`dbx:",inline"` → 子字段沿用自己的列名，不加前缀；此时若与父结构体其它列撞名，解析期报错（见 ⑦）。

```go
type Person struct {
	ID      int64   `dbx:"id,autopk"`
	Address Address `dbx:"addr_,inline"`   // → addr_city、addr_street
}
type Person2 struct {
	ID      int64   `dbx:"id,autopk"`
	Address Address `dbx:",inline"`         // → city、street（不加前缀）
}
```

> 首段是**前缀**而不是列名，所以当结构体字段的列名恰好与某个选项名相同（`inline`、`null` 等）时会被当成选项解析。此时用带下划线的前缀写法（`dbx:"inline_,inline"`）或 `dbx:",inline"` 规避。

**④ 多级嵌套与前缀链**

前缀沿嵌套链**逐级累加**：

```go
type Outer struct {
	Inner Inner `dbx:"in_,inline"`   // → out_in_code
}
type Inner struct {
	Code string `dbx:"code"`
}
type Model struct {
	Outer Outer `dbx:"out_,inline"`
}
```

最终列名 `out_in_code`（= `out_` + `in_` + `code`）。

**⑤ 嵌套结构不参与写入**

整个嵌套结构不参与时写 `dbx:"-"`（**整组**都不产生列）：

```go
Address Address `dbx:"-"`               // addr_city / addr_street 都不存在
```

嵌套结构**字段本身**只认 `col` 与 `inline`（写别的选项解析期报错）—— `omit` 这类"随值变化"的选项写在结构字段上没有意义：子列要么恒参与，要么整个结构不参与（见 R4）。子列各自成列，可以单独写 `omit`（UPDATE 的 SET 阶段零值跳列，见 [prd-update.md](prd-update.md) R10）。

**⑥ 指针型嵌套结构**

字段是 `*Address` 时：

- 指针为 nil → 整组按零值处理（子列写零值）；
- 读取路径上遇到 nil 指针不会 panic。

**⑦ 列名冲突**

展开后若出现两个同名列（例如父结构体有 `city`，嵌套结构无前缀也有 `city`），**解析期报错**，报出冲突的列名。

**⑧ 完整示例**

```go
type Address struct {
	City   string `dbx:"city"`
	Street string `dbx:"street"`
}

type Person struct {
	ID        int64     `dbx:"id,autopk"`
	Name      string    `dbx:"name"`
	Address   Address   `dbx:"addr_,inline"`
	Age       int       `dbx:"age,null"`
	CreatedAt time.Time `dbx:"created_at,deff=now"`
}
```

`p := &Person{Name: "aaa", Address: Address{City: "SH", Street: "Road"}}`

```sql
INSERT INTO "person"("name","addr_city","addr_street","age","created_at") VALUES(?,?,?,?,?)
-- 参数：["aaa", "SH", "Road", NULL, 2026-10-23T...]
```

（`id` 是自增主键不进 INSERT；`age` 标了 `null`，零值写成 `NULL`；`created_at` 由默认值函数补齐并写回结构体。）

**⑨ 整对象存成一列（`json`）**

不想拆列、想把整个子对象序列化成一个 JSON 字符串存进单独一列时，给该字段加 `json`：

```go
type Person struct {
	ID      int64   `dbx:"id,autopk"`
	Address Address `dbx:"payload,json"`   // → 一列 payload
}

p := &Person{Address: Address{City: "SH", Street: "Road"}}
```

```sql
INSERT INTO "person"("payload") VALUES(?)
-- 参数：["{\"city\":\"SH\",\"street\":\"Road\"}"]
```

规则：

1. `json` 字段**不参与嵌套展开** —— 它就是单独一列，列名照常推导（`dbx:"payload,json"` → `payload`；`dbx:",json"` → 字段名的 snake_case）；
2. 写入时整个值用 `encoding/json` 序列化成字符串；**序列化成什么形态由该类型自己的 `json` 标签决定**（不是 `dbx` 标签 —— `dbx` 描述数据库列，`json` 描述 JSON 形态，两者互不干涉）；序列化失败（值放不进 json 列）挂 `dbwm.ErrTypeMismatch` 哨兵，业务侧按"类型与值不匹配"判定（见 [prd-error.md](prd-error.md) R5）；
3. 字段是 `*T` 且为 nil → 该列写 SQL `NULL`（不是字符串 `"null"`）；
4. `json` 与 `inline` **互斥**，同时写解析期报错；
5. `json` 列不参与列名前缀链，它就是一个普通列；`null` 对它不适用（`json` 与 `null` 互斥）。

> 这是行业里常见的两种建模方式：**拆列**（`inline`）存成多个可查询、可索引的列；**整列**（`json`）存成一个 JSON 文档列（PostgreSQL 的 `jsonb`、MySQL 的 `json`）。按查询需求选：需要按字段检索/建索引用 `inline`，整体存取、结构会变用 `json`。

**验收**

- `Person{Name:"aaa"}` → 列含 `name`、`addr_city`、`addr_street`、`age`，不含 `id`；
- 嵌套结构字段没写 `inline` → 整组不产生任何列；
- 展开后列名重复 → 解析期报错并指出冲突列名；
- `dbx:"payload,json"` → 只产生一列 `payload`，参数是序列化后的 JSON 字符串；
- `dbx:"payload,json,inline"` → 解析期报错。

### 4.3 单列写入策略

#### R4 零值与 NULL（`null`）

**需求**：每个可写字段都要有确定的落库形态 —— 零值写 Go 零值，还是写 SQL `NULL`，由模型声明。

**规则**：

1. **所有参与解析的字段都进 INSERT**（`dbx:"-"` 的字段与未标注 `dbx` 的字段除外；自增列零值时除外）——
   列清单是模型的**静态属性**，不再随数据变化，因此单条插入与批量插入的列清单完全一致；
2. 值是**零值**时：
   - 没标 `null` → 写 **Go 零值**（`0` / `''` / `false` / 零值时间）；
   - 标了 `null` → 写 **SQL `NULL`**；
3. 值**非零**时：写原值（`null` 不影响非零值）；
4. 指针字段为 `nil` → 写 `NULL`（**不需要** `null` 选项）；指针非 nil 且指向零值 → 写那个零值（"显式赋过零值"）；
5. `null` 同时表达 DDL 语义：这一列**可空**；
6. `null` 与 `autopk` / `json` 互斥（解析期报错）：自增列零值交给数据库生成，JSON 列的值是序列化后的字符串；
7. `null` 与 `deff` / `defv` 同时出现时**默认值优先**：先补默认值，补完不再是零值，写的是默认值；
8. `omit` 在 **INSERT 里不生效**（列清单恒定，零值照写）；它在 UPDATE 的 SET 阶段生效（零值列不进 `SET`，见 [prd-update.md](prd-update.md) R10）。不参与写入用 `dbx:"-"`，零值写 `NULL` 用 `null`。

**零值怎么判定**：

| 字段类型 | 零值 |
| --- | --- |
| 基本类型 | Go 零值（`0` `""` `false`） |
| 指针 / 接口 | `nil` |
| 切片 / 映射 | `nil` 或长度为 0 |
| `time.Time` | `IsZero()` 为真，即 **`0001-01-01T00:00:00Z`** |
| 结构体 | 所有字段都是零值 |

> `time.Time` 单独说明：数据库里的 timestamp 零值是 `0001-01-01 00:00:00`，读回 Go 之后 `wall`/`ext`/`loc` 三个私有字段不再全零，用 `reflect` 的结构体零值判定会失灵。因此统一按**时刻**判定：`t.IsZero()` 为真就算零值。业务上的"零时间"如果另有含义，请用指针字段表达。

> **取舍**：代价是数据库的列 `DEFAULT` 不再有机会生效 —— 列一旦出现在 INSERT 里，`DEFAULT` 就不会触发。
> 需要"这一列由数据库填"（如 `created_at DEFAULT CURRENT_TIMESTAMP`、数据库侧生成的 UUID），
> 就在模型里**不声明**这个字段（`dbx:"-"`），把列交给 DDL 与数据库负责。

**验收**：

- `Age int \`dbx:"age"\``，`Age == 0` → INSERT 里有 `age` 列，值是 `0`；
- `Age int \`dbx:"age,null"\``，`Age == 0` → INSERT 里有 `age` 列，值是 `NULL`；`Age == 18` → 值是 `18`；
- `At time.Time \`dbx:"at,null"\``，`At` 是 `0001-01-01` → 值是 `NULL`；
- `Name *string \`dbx:"name"\``，`Name == nil` → 值是 `NULL`；`Name = &""` → 值是空串。

#### R5 默认值（`deff=` 默认值函数 / `defv=` 默认值）

**需求**：零值字段需要一个默认值，来源有两种。

**规则**

① **默认值函数 `deff=<函数名>`**

1. 默认值来源是**注册表里的 Go 函数**，不是 SQL 片段：
   ```go
   dml.RegisterDefault("now", func(ctx context.Context) (any, error) {
       return time.Now().UTC(), nil
   })
   ```
2. 字段标注 `deff=<函数名>` 且值为零值 → 调用该函数；
3. **取值写回结构体字段**，并作为绑定参数写入该列；
4. 函数可以读 `ctx`（当前用户、租户、trace id 等）；
5. 函数未注册 → 报错并指出函数名；函数返回错误 → 报错并带上函数名；
6. **默认值只在写包自身的 INSERT 时生效**；UPDATE 的取值语义由 [prd-update.md](prd-update.md) R4 定义 —— 门面的 Update 对 `deff=` 同样生效（零值才补，审计列刷新的落点，见 prd-slyorm R12），`defv=` 不生效。
7. 与 `null` 同时出现时**默认值优先** —— 补完值不再是零值，写的是默认值而不是 `NULL`（见 R4）。

> 为什么不支持 `deff=now()` 这种 SQL 片段：值拿不回结构体（要靠 `RETURNING`）、跨方言要各写一遍、有拼接风险。数据库端的默认值（`DEFAULT now()`）属于建表侧，用 `default=` 表达。

② **默认值 `defv=<字面量>`**

1. `defv` 的值是**常量字面量**，按字段类型解析：

   | 字段类型 | 写法 | 示例 |
   | --- | --- | --- |
   | 整型 | 十进制 | `defv=18` |
   | 无符号整型 | 十进制 | `defv=0` |
   | 浮点 | 十进制 | `defv=1.5` |
   | 布尔 | `true/false/1/0/yes/no/on/off` | `defv=true` |
   | 字符串 | 原样（可加引号） | `defv='unknown'` |
   | `time.Time` | 多种候选格式，见下表 | `defv=2026-01-02T03:04:05Z`、`defv=2026-01-02 03:04:05` |
   | `[]byte` | 原样 | `defv=abc` |

   `time.Time` 的候选格式（按顺序尝试，命中第一条即为结果）：

   | 候选 | 示例 | 说明 |
   | --- | --- | --- |
   | RFC3339 | `2026-01-02T03:04:05Z`、`2026-01-02T03:04:05+08:00` | 带小数秒的写法（`2026-01-02T03:04:05.123Z`）也在这里命中 |
   | 无时区的 ISO | `2026-01-02T03:04:05` | 按 UTC 解析 |
   | DateTime | `2026-01-02 03:04:05` | 按 UTC 解析；带小数秒也可以 |
   | 带偏移的 DateTime | `2026-01-02 03:04:05+08:00` | 墙上的钟点是 03:04:05（不是把 UTC 时刻搬到 +08:00） |
   | 斜杠写法 | `2026/01/02 03:04:05` | 按 UTC 解析 |
   | DateOnly | `2026-01-02` | 当天 00:00:00 UTC |
   | Unix 时间戳 | `1767236645`、`1767236645123` | 纯数字，**位宽决定单位**：10 位秒、13 位毫秒、16 位微秒、19 位纳秒 |

   > **没有时区信息的写法一律按 UTC 解析** —— 同一份模型元信息在任何机器上都得到同一时刻，不会随本机时区漂移。
   > Unix 时间戳的**位宽必须精确命中**标准宽度：`20260102` 这类紧凑日期仍然是非法的时间字面量，不会被当成秒级时间戳。

2. **值的处理**（`=` 之后的文本）：
   - 先去掉**首尾空白**再处理（`defv= 18 ` → `18`）；
   - **引号是分组标记**（`'` 或 `"`）：

     | 写法 | 值 | 说明 |
     | --- | --- | --- |
     | `defv='hello world'` | `hello world` | 引号内的内容整体作为字符串（空格、逗号都不再特殊） |
     | `defv="abc"` | `abc` | 双引号同理 |
     | `defv='it''s'` | `it's` | 成对引号表示一个引号 |
     | `defv=pre'quoted'post` | `pre'quoted'post` | 没有外层引号，内容原样保留 |
     | `defv=it's` | `it's` | 同上（引号夹在中间，不动） |

     > 要在字符串里保留引号，必须成对写出：`defv='it''s'`。`defv` 只按基础类型处理，不支持列表等复杂写法。
     > **去引号是解析选项时的统一动作**（对所有选项一视同仁），不是 `defv` 独有：取值里成对的外层引号都会被脱掉，`default='x'` 存进 `Opts` 的是 `x` —— DDL 侧要自己补引号。
   - 引号处理完得到的文本**再按字段类型转换** —— 标签里的一切都是**字符串**，标在 `int` 字段上就要能转成整数，转不了就报错（并提示"默认值函数请改用 `deff=`"）；
   - **未指定值**（`defv` 或 `defv=`）时按空字符串处理：字符串 / `[]byte` 直接用空值；其它类型无法从空串转换 → 解析期报错；
3. 字面量在**解析期**就校验，类型不匹配立刻报错（不等到 INSERT）；
4. 取值同样**写回结构体字段**；
5. `deff` 与 `defv` 只能二选一，同时出现解析期报错。

**验收**

- `CreatedAt time.Time \`dbx:"created_at,deff=now"\`` → 插入后 `p.CreatedAt` 非零，且数据库里存的是同一个值；
- `Age int \`dbx:"age,defv=18"\`` → `Age == 0` 时插入 18，且 `p.Age == 18`；
- `Age int \`dbx:"age,defv= 18 "\`` → 与 `defv=18` 等价；
- `Note string \`dbx:"note,defv="\`` → 写入空串；
- `Name string \`dbx:"name,defv='it''s'"\`` → 写入 `it's`；
- `Name string \`dbx:"name,defv='a,b'"\`` → 写入 `a,b`（引号内的逗号不切分）；
- `defv=abc` 标在 `int` 字段上 → 解析期报错。
- `CreatedAt time.Time \`dbx:"created_at,defv=2026-01-02 03:04:05"\`` → 解析为 `2026-01-02T03:04:05Z`；
- `CreatedAt time.Time \`dbx:"created_at,defv=1767236645"\`` → 解析为同一个时刻（秒级 Unix 时间戳）；
- `CreatedAt time.Time \`dbx:"created_at,defv=20260102"\`` → 解析期报错（位宽不是标准时间戳宽度）。

#### R6 自增主键（`autopk`）与回填

**需求**：自增主键的值为零时不参与 INSERT，由数据库生成，成功后回填到结构体；调用方给了显式值则按显式值写入。

**规则**：

1. **自增主键只有 `autopk` 一种写法**：`dbx:"id,autopk"`；
   - `autopk` 同时表达"是主键"和"自增"；
   - 一个模型**只能有一个** `autopk` 列，多写解析期报错；
   - `pk`（主键组）与 `auto`（普通自增列）**不是本包的选项** —— 出现在标签里被忽略（保留到 `Opts`），不报错、不改变写入行为；
     换句话说 `dbx:"id,pk,auto"` **不等于** `dbx:"id,autopk"`：`id` 只是个普通列，零值照写 `0`；
2. 自增列**值为零**时不进入 INSERT 的列清单（值非零时进入，见 ⑥）；
3. 插入成功后取回自增值（**方式由方言决定**，见 R8 与 §3.3）：
   - SQLite / MySQL：`LastInsertId`；
   - PostgreSQL：`RETURNING "id"` —— 语句里带 RETURNING 子句，从结果集里按行序取回；
4. **只有 `autopk` 列会回填**自增值，插入后 `p.ID` 立刻可用；
   - 其它主键（不带 `autopk`）**不回填** —— ID **由调用方自己指定**；
5. 批量插入（多 VALUES）按行序回填 `lastID-n+1 … lastID`；
6. **显式值**（这一条取代了原来的 `force` 选项）：

   | 自增列的值 | 是否进入 INSERT | 是否回填自增值 |
   | --- | --- | --- |
   | 零值 | ❌ | ✅ 回填数据库生成的值 |
   | 非零（调用方显式赋值） | ✅ 写入这个显式值 | ❌ 不覆盖显式值 |

   - 这条规则让"显式插入主键"不需要额外选项：`p := &Person{ID: 42}` → INSERT 里带 `id=42`，插入后 `p.ID` 仍是 `42`；
   - `force` 因此被移除：本包不认它（忽略但保留到 `Opts`，见 R11），要写显式值直接把值赋给字段。

**验收**

- `ID int64 \`dbx:"id,autopk"\`` → INSERT 里没有 `id` 列；插入后 `p.ID > 0`，且能按该 ID 查到这条记录；
- `ID int64 \`dbx:"id,autopk"\`` + `ID = 42` → INSERT 里有 `id` 列且值为 `42`；插入后 `p.ID == 42`（不被自增值覆盖）；
- `ID int64 \`dbx:"id,pk,auto"\`` → 不报错；`id` 是**普通列**（零值写 `0`，不回填）—— `pk` 与 `auto` 都不是本包的选项。

#### R7 主键组（`pk`）—— 不在 INSERT 范围

**需求**：明确哪些列是主键（属于 UPDATE/DELETE 的需求，本 PRD 只说明写包怎么对待它）。

**规则**：

- `pk` **不是写包的选项** —— 出现在标签里被忽略（保留到 `Opts`），不报错；
- 主键组（裸 `pk`）在 model 层是**主键标记**（供门面 Collection 的 Update 剔除 SET，见 [prd-update.md](prd-update.md) R6）；`pk=<组名>` 的组名不认（保留在 `Opts`），复合主键用多个裸 `pk` 表达；写包行为不变 —— 主键列照常参与 INSERT；
- 自增主键用 `autopk` 表达（见 R6），它已经包含主键语义；
- 对写包来说，主键列就是个**普通列**：照常参与 INSERT，主键值由调用方自己指定（`autopk` 除外 —— 零值时由数据库生成）。

**验收**：`OrderID int64 \`dbx:"order_id,pk=pk_order_item"\`` → 不报错，`pk` 保留在 `Opts` 里，该列照常参与 INSERT。

### 4.4 语句生成与执行

#### R8 SQL 形态与参数绑定

**需求**：生成的 SQL 必须可预期，且值一律走参数绑定。

**规则**：

1. 语句形态：`INSERT INTO "<表>"("<列1>","<列2>",...) VALUES(?,?,...)`；
2. **所有列名与表名都加引号**（引号风格由方言决定：SQLite / PostgreSQL 用 `"`，MySQL 用 `` ` ``）；
3. **所有值都是绑定参数**，不拼进 SQL 字符串；
4. 占位符由方言决定（SQLite/MySQL `?`，PostgreSQL `$1,$2,...`；多组 VALUES 时编号连续）；
5. 列的顺序 = 模型字段的声明顺序（嵌套结构按其字段位置展开）；
6. 表名带前缀时：`INSERT INTO "public"."person"(...)`；
7. `json` 列的值是序列化后的 JSON 字符串，同样走参数绑定（不拼进 SQL）；
8. **列清单是模型的静态属性** —— 除了未标注 `dbx` 的字段、`dbx:"-"` 的字段与自增列（零值时不进），其余字段都进 INSERT；
   因此批量插入时各行列清单天然一致（见 R9）。
9. **业务规则与数据库无关** —— 列清单、零值语义、默认值、自增回填这些规则对所有数据库一样；
   方言只影响 SQL 形态与"取回数据库生成的值"的方式（见 §3.3）。
10. **打开时可以自动识别库型** —— 识别不出来就报错，由调用方显式指定方言（不猜）。

**验收**：`BuildInsert` 返回的 SQL 字符串与参数数组，参数顺序与列顺序一一对应。

#### R9 批量插入

**需求**：一次插入多条记录。

**规则**：

1. API 接受切片（`[]T` 或 `*[]T`）；
2. 生成**多 VALUES 的 SQL**：`VALUES(?,?),(?,?),(?,?)`；
3. **各行的列清单天然一致** —— 列清单是模型的静态属性（见 R4），不随每行的值变化；
   唯一的例外是**自增列**：要么整批都为零值（由数据库生成），要么整批都显式赋值；
   混用时报错并指出第几行不一致；
4. 零值不打断批量：标了 `null` 的列，零值行写 `NULL`，非零值行写原值，**列仍然对齐**；
5. 自增值按行序回填（见 R6.5）；
6. 空切片直接返回，不报错；
7. **参数上限自动分块**：一条 SQL 的绑定参数个数有上限（SQLite 默认 32766），
   列数 × 行数超限时自动拆成多条语句执行，对调用方透明；
   事务外的多条语句不保证原子 —— 要么包在 `db.Tx` 里，要么接受部分成功。

**验收**：3 条记录 → 1 条 SQL、3 组 VALUES、影响行数 3、三个元素的 ID 依次回填；
第 1 行 `age` 为零、第 2 行 `age` 为 20 → 两条记录都写 `age` 列，值分别是 `NULL` 和 `20`；
第 2 行给了自增主键、第 1 行没给 → 报错并指出不一致的行号。

#### R10 事务

**需求**：一次业务操作涉及多张表时，必须原子。

**规则**：

1. 事务对象挂在 `ctx` 上：
   ```go
   err := db.Tx(ctx, func(ctx context.Context) error {
       if _, err := db.Insert(ctx, &person); err != nil { return err }
       if _, err := db.Insert(ctx, &order);  err != nil { return err }  // 另一张表
       return nil
   })
   ```
2. 回调返回错误 → 自动 `Rollback` 并返回该错误；返回 `nil` → `Commit`；
   回调 **panic** → 先 `Rollback` 再把 panic 原样抛回；
3. **嵌套调用复用外层事务**：回调里再调 `db.Tx` 不会新开事务，
   提交/回滚统一由最外层决定（REQUIRED 传播语义）；
4. 业务代码不需要给每个方法传 `tx` 参数；
5. 事务外的 `db.Insert` 走连接池，不受影响；
6. 同一事务内可写多张表、各写多行。

**验收**：事务内插入后主动返回错误 → 表内无该记录；返回 nil → 记录存在。

### 4.5 错误处理

#### R11 错误处理与可诊断性

**需求**：写错了要能立刻定位。

**规则**：

1. **解析期错误**（结构体定义有问题）在第一次使用该模型时就报出来，错误信息包含：结构体名、字段名、选项名、正确写法；
2. **运行期错误**（默认值函数失败、SQL 失败）原样向上返回，附上下文；
3. 未知选项（既不是核心选项也没注册过）**忽略但保留**到列的 `Opts`，不阻断业务；业务可用 `RegisterOption` 注册自定义选项；
4. 写规则里出现读规则专属选项（`op=` `comb=` 等）→ **忽略但保留到 `Opts`**（它们由查询包解释，写包不报错）；
5. 语义冲突的选项组合（`deff`+`defv`、`json`+`inline`、`null`+`autopk`、`null`+`json`）→ **报错**并指出冲突的两个选项；
6. 同一选项重复出现 → **报错**（视为笔误，不做"后者覆盖前者"）；
7. **已移除的选项**（`omitempty` `force` 等）→ **忽略但保留到 `Opts`**（本包没有它们的语义，不报错、不静默丢弃）；`omit` 写包认它（取值按布尔校验），但只在 UPDATE 的 SET 阶段生效（[prd-update.md](prd-update.md) R10），INSERT 不受影响。

> 规则 4 与 7 是同一件事：**写包只认自己这套选项**，其余的一律"忽略但保留"。
> 保留而不是丢弃，是为了 DDL 生成器、文档生成器、别的插件还能读到"这里写过 `omitempty`"这个事实。

**验收**

- `dbx:"id,omitempty"` → 不报错，`omitempty` 保留在 `Opts` 里（写零值靠 `null`，不参与写入靠 `dbx:"-"`）；
- `dbx:"tok,omit"` → 不报错，`omit` 保留在 `Opts` 里（INSERT 不生效；UPDATE 的 SET 阶段零值跳列）；
- `dbx:"tok,omit=maybe"` → 报错并指出 `omit` 的取值不是合法布尔值；
- `dbx:"id,force"` → 不报错，`force` 保留在 `Opts` 里（自增列要写显式值直接把值赋给字段）；
- `dbx:"a,op=like"` → 不报错，`op` 保留在 `Opts` 里；
- `dbx:"id,pk,pk"` → 报错并提示选项重复；
- `dbx:"payload,json,inline"` → 报错并指出 `json` 与 `inline` 冲突；
- `dbx:"id,autopk,null"` → 报错并指出 `autopk` 与 `null` 冲突；
- `dbx:"age,mycustom=1"` → 不报错，且 `mycustom` 保留在 `Opts` 里；
- 字段未标注 `dbx` → 不报错，整个字段被忽略（不产生列，列名也不推导）。

---

## 5. 字段 → 是否参与 INSERT 对照表

| 字段形态 | 是否进入 INSERT | 零值时写什么 |
| --- | --- | --- |
| 普通字段，值非零 | ✅ | —— （写该值） |
| 普通字段，值零 | ✅ | Go 零值：`0` / `''` / `false` / 零值时间 |
| 普通字段 + `null`，值零 | ✅ | SQL `NULL` |
| 普通字段 + `null`，值非零 | ✅ | —— （写该值） |
| 指针字段，为 nil | ✅ | SQL `NULL`（不需要 `null` 选项） |
| 指针字段，非 nil 指向零值 | ✅ | 那个零值（"显式赋过零值"） |
| `autopk` 字段，值零 | ❌ | —— （不进 INSERT；成功后回填自增值） |
| `autopk` 字段，值非零 | ✅ | —— （写显式值，不回填） |
| 标了 `auto` / `pk` 的字段 | ✅ | Go 零值 —— 这两个选项**不是本包的选项**，被忽略（保留到 `Opts`） |
| 有 `deff`，值零 | ✅ | 先调默认值函数补值 |
| 有 `deff`，值非零 | ✅ | —— （用原值，不调函数） |
| 有 `defv`，值零 | ✅ | 先写常量默认值 |
| 有 `defv`，值非零 | ✅ | —— （用原值） |
| 有 `deff`/`defv` + `null`，值零 | ✅ | 默认值优先：写默认值（不是 NULL） |
| `omit` 字段，值零 | ✅ | Go 零值 —— `omit` 在 INSERT 不生效（列清单恒定，见 [prd-update.md](prd-update.md) R10） |
| 嵌套结构子字段（`inline`） | ✅ | Go 零值；列名带前缀 |
| `json` 字段，值非零 | ✅ | —— （整一列，值是 JSON 字符串） |
| `json` 字段，值为零 | ✅ | 写序列化结果（`{"city":"","street":""}`） |
| `json` 字段（`*T`），为 nil | ✅ | SQL `NULL` |
| 结构体字段，**没写 `inline`** | ❌ | 整个字段忽略 |
| **未标注 `dbx`** 的字段 | ❌ | 整个字段忽略（连列名都不推导） |
| `dbx:"-"` | ❌ | 完全忽略 |

---

## 6. API 一览

```go
// 打开数据库：先按驱动名映射方言，映射不到再探测
db, _ := insert.Open("sqlite", "file:app.db")
// 也可以显式指定方言（构造选项）：insert.Open("mysql", dsn, dbwm.WithDialect(dbwm.MySQL))
// 或注入已有的连接池：insert.NewSQL(base, dbwm.WithDialect(dbwm.PostgreSQL)) / insert.New(conn)

stmt, vals, _ := db.BuildInsert(ctx, &p)   // 只生成 SQL 与参数，不落库
res, err := db.Insert(ctx, &p)             // 插入一条
res, err := db.InsertAll(ctx, &people)     // 批量插入（切片）
err = db.Tx(ctx, func(ctx context.Context) error {   // 事务（可跨表）
    ...
})

insert.RegisterDefault("now", fn)          // 注册默认值函数（配合 deff=now）
insert.RegisterTable[Person]("person")     // 登记表名
```

`Result`：`RowsAffected`（影响行数）、`LastInsertID`（自增值）。

---

## 7. 已知限制与待拍板

**限制**

1. **库型识别先按驱动名、后探测**：`Open` 拿驱动名直接映射方言（`sqlite`/`sqlite3`、`mysql`/`mariadb`、`postgres`/`pgx`），映射不到才连库探测（`version()` / `sqlite_version()`），省一次探测连接；MySQL 系的 MariaDB、TiDB 都归到 MySQL 方言；
   探测时认不出的版本串（如 CockroachDB）**直接报错，不猜** —— 猜错方言会让 RETURNING / 自增回填静默失效；
2. **探测要真的连一次库** —— 映射不到驱动名时，打开时库连不上会直接报错（不会等到第一次写入才发现）；
3. 嵌套结构只展开到**同一张表**，不支持"一个字段对应另一张表"；
4. 嵌套结构上写规则只支持 `inline`（以及 `dbx:"-"` 忽略整组），其它选项报错；
5. 批量插入各行列清单天然对齐；**唯一例外是自增列**（整批全零或整批全显式，否则报错，见 R9.3）；
6. 未实现"主键必填"校验（插入后主键全空是否报错）；
7. `json` 列目前只定义了**写入**语义 —— 读回来的 JSON 字符串如何还原成结构体不在本文档讨论范围；
8. **只有 `autopk` 会回填自增值** —— 其它主键 ID 由调用方自己指定；
9. **INSERT 不做 DDL 字段类型约束** —— 列的类型、可空性由建表语句保证，ORM 只负责生成 INSERT；
10. **写入钩子不在本 PRD 范围** —— 原型代码里已有实现（模型级 `BeforeInsert`/`AfterInsert`/`Validator` + 全局 `db.Use`），但需求（执行顺序、错误传播、与事务的关系）待重新拍板，本文暂不承诺；
11. **数据库的列 `DEFAULT` 不再有机会生效** —— 所有列恒进 INSERT，`DEFAULT` 不会触发；
    需要数据库填的列（如 `created_at DEFAULT CURRENT_TIMESTAMP`）就不在模型里声明，交给 DDL 与数据库负责；
12. **写包不认的选项不会报错** —— 读规则选项（`op=` `comb=` …）、已移除选项（`omitempty` `force`）与不属于写包的选项（`pk` `auto`）一律忽略但保留到 `Opts`；误写的选项名只能靠"读得懂 `Opts`"或将来的 `CheckTags()` 体检发现；
13. **MySQL 批量插入不回填自增主键** —— MySQL 多行 INSERT 的 `LastInsertId` 是**第一行**的值（与 SQLite 的"最后一行"相反），且 MySQL 8 起 `innodb_autoinc_lock_mode` 默认为 2（interleaved），并发下一条语句分到的自增值不保证连续；批量 + `autopk` 零值时**报错**引导（逐条插入 `db.Insert`，或给自增列显式赋值），不回填可能错位的值；单条插入不受影响；
14. **MySQL 不接受零值时间** —— `time.Time` 零值（`0001-01-01`）超出 MySQL `DATETIME` 最小值（`1000-01-01`），未标 `null`、未配 `deff=` 的零值时间字段在 MySQL 上会报 Error 1292；MySQL 用户的时间字段请标 `null` 或配 `deff=now`；
15. **`json` 列在 PostgreSQL 上建议 DDL 用 `text`** —— 写入侧绑定的参数是字符串；列建成 `jsonb` 时 `lib/pq` 驱动可能报 42804（text 与 jsonb 无隐式转换），`pgx` 通常能按上下文推断；对应"待拍板 1"。

**已拍板（本轮结论）**

| 议题 | 结论 |
| --- | --- |
| 整对象存一列 | `json` 选项：整个值序列化成 JSON 字符串，存成单独一列；`inline` 拆成多列。两种都是常见做法，按查询需求选 |
| 前缀多级拼接 | 允许 —— `dbx:"a_b_,inline"` 的第一段按字符串处理，拼接成 `a_b_<子列>` |
| `defv` 的值处理 | `=` 之后去掉首尾空白；引号是分组标记（外层引号脱掉，成对引号表示一个引号，中间的引号原样保留）；再按字段类型转换；未指定值时字符串用空串，其它类型报错 |
| `defv` 的能力边界 | 只按基础类型处理，不支持列表等复杂写法；要在字符串里保留引号必须成对写出（`defv='it''s'`） |
| `time.Time` 的零值 | 按 `IsZero()` 判定 —— `0001-01-01`（数据库 timestamp 的 0）算零值 |
| `force` | **已移除** —— 自增列的显式值靠"值非零即写入"表达；UPDATE 要写零值用指针字段；本包不认它（忽略但保留到 `Opts`） |
| `pk` / `auto` 的位置 | **不属于写包的选项** —— 主键组只在 UPDATE/DELETE 里用于定位，普通自增列在本包没有意义；出现在标签里被忽略（保留到 `Opts`），不报错。自增主键一律写 `autopk` |
| 自增值回填范围 | 只有 `autopk` 回填 |
| `omit` | **INSERT 里不生效** —— 列清单恒定，零值照写；零值要写 `NULL` 用 `null`，不参与写入用 `dbx:"-"`；写包认这个选项（取值按布尔校验），它在 UPDATE 的 SET 阶段生效（零值列不进 `SET`，见 [prd-update.md](prd-update.md) R10） |
| 零值的落库形态 | 零值写 Go 零值是默认；标了 `null` 的列零值写 SQL `NULL`；指针为 nil 一律写 `NULL` |
| 批量插入的对齐 | 列清单是模型的**静态属性**（除自增列），天然对齐 —— 不再需要"各行一致否则报错"的兜底 |
| `json` 的范围 | 只管写入（INSERT 侧序列化）；读取侧的反序列化不在讨论范围 |
| DDL 约束 | INSERT 不考虑 DDL 字段类型约束 |

**待拍板**

1. `json` 列在 DDL 层要不要约束成 `jsonb` / `text`？
2. 需要"由数据库填默认值"的列，用 `dbx:"-"`（模型里不声明）是否够用？要不要另开一个"DB 管"标记？
3. 写入钩子的最终形态：执行顺序、错误传播、与事务的边界？
4. 是否需要"主键必填"的运行时校验（插入后主键组全空是否报错）？
5. `defv` 是否需要支持 `[]string` 这类复合类型的字面量？
