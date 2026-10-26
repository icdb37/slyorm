# 数据库错误分类（error）需求文档

> 覆盖范围：数据库执行错误的**类别判定**与业务侧处理接口。
> 落地位置：`dbwm`（错误类型与识别）+ 根包 `slyorm`（业务侧判定接口）。

## 目录

- [1. 目标与范围](#1-目标与范围)
- [2. 术语](#2-术语)
- [3. 分层与职责边界](#3-分层与职责边界)
- [4. 需求条目](#4-需求条目)
- [5. API 一览](#5-api-一览)
- [6. 与既有包的关系](#6-与既有包的关系)
- [7. 已知限制](#7-已知限制)
- [8. 已拍板](#8-已拍板)
- [9. 实现参考](#9-实现参考)

---

## 1. 目标与范围

### 1.1 目标

业务侧拿到数据库报错后，能**判定它属于哪一类**并据此处理 —— 唯一索引冲突提示"已存在"、字段类型与值不匹配判参数错误、其余归内部错误 —— 不必去解析驱动错误文本，也不必知道底下用的是哪个数据库。

### 1.2 在范围内

1. 错误**类别**（Kind）的定义与对应的哨兵错误；
2. 驱动错误 → 类别的**识别规则**（SQLite / MySQL / PostgreSQL，另有通用兜底）；
3. 类别信息的**承载类型**（`*dbwm.Error`：类别、动作、约束名、出错语句、底层错误）；
4. 业务侧的**判定接口**（根包 `slyorm`）：`IsUnique` / `IsTypeMismatch` / `IsNotNull` / `IsForeignKey` / `IsCheck` / `ErrorKind` / `ConstraintOf`；
5. 读侧**值放不进目标字段**的归类（扫描期的类型不匹配与写侧归为一类）。

### 1.3 不在范围内

1. **不做重试策略** —— 重试是业务的事，框架不自动重试；
2. **不做 HTTP 状态码映射** —— 框架不认识 Web 层，"冲突 → 409"由业务自己定；
3. **不做模型侧的字段类型校验** —— 模型只声明列名与写规则，没有列类型信息，值与类型是否匹配由数据库判定（见 §7 限制 1）；
4. **不改写底层驱动错误** —— 驱动错误原文保留在错误信息里，并可 `errors.As` 穿透；
5. **不引入驱动依赖** —— 本库不 import 任何驱动，识别靠错误码 / SQLSTATE / 错误文本特征；
6. **不把查询未命中当错误** —— 查不到记录是正常结果，不进类别集合，框架不做错误处理（见 R6）。

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| 类别（Kind） | 数据库错误的归类：唯一冲突 / 类型不匹配 / 非空 / 外键 / CHECK / 其他 |
| 约束（Constraint） | 报错的约束名或冲突列（唯一索引名、主键名、列名），驱动给得出才有 |
| 动作（Action） | 出错的动作：执行 INSERT 语句、执行查询、提交事务… |
| 驱动错误 | 底层 `database/sql` 驱动返回的原始错误 |
| 未命中（No Rows） | 查询没有命中任何行 —— 正常结果，不是数据库错误（见 R6） |

---

## 3. 分层与职责边界

```
业务 → slyorm（判定接口） → dbwm（错误类型、识别、执行） → 驱动
```

| 层 | 职责 |
| --- | --- |
| `dbwm` | 错误类型（`*dbwm.Error`）、类别定义与哨兵、驱动错误的识别 —— 错误在执行路径上产生，驱动错误的形态只有执行层看得见 |
| `slyorm`（根包） | 业务侧判定接口（谓词 + 取类别 / 约束名）—— 业务只 import 门面，不必碰中间层 |
| `rows` | 读侧扫描错误的归类：值放不进目标字段是读侧的事实，由它挂类别哨兵 |

---

## 4. 需求条目

### R1 错误类型与类别

**需求**：业务侧能判定类别，不用解析错误文本。

**规则**：

1. 类别集合固定七种：

   | 类别 | 含义 |
   | --- | --- |
   | `KindUnknown` | 不是数据库错误（或认不出）—— 框架自身的错误落这里 |
   | `KindUnique` | 唯一索引 / 主键冲突 |
   | `KindTypeMismatch` | 字段类型与值不匹配（值放不进列 / 放不进目标字段） |
   | `KindNotNull` | 非空约束不满足（列没给值且库里没有默认值） |
   | `KindForeignKey` | 外键约束不满足（引用了不存在的父行，或删了被引用的行） |
   | `KindCheck` | CHECK 约束不满足 |
   | `KindOther` | 是数据库错误，但不属以上任何一类 |

2. 每个具体类别配一个**哨兵错误**（`dbwm.ErrUnique` 等），`errors.Is` 可判定；另有一个"是数据库错误"的哨兵 `dbwm.ErrDatabase` —— 任何已分类的数据库错误对它都为真；
3. 类别信息装在 `*dbwm.Error` 里，字段：`Kind`（类别）、`Action`（动作）、`Constraint`（约束名 / 冲突列）、`Statement`（出错语句）、`Err`（底层驱动错误）；
4. `*dbwm.Error` 保留底层错误：`Unwrap` 返回 `Err`，`errors.As` 能拿到驱动自己的错误类型；
5. 错误信息格式：`dbwm: <动作>失败：<类别中文名>（约束 <约束名>）：<驱动错误原文>；语句: <语句>` —— 类别与约束名各出现一次即可定位；认不出约束名时省掉括号部分；
6. 类别判定接口（谓词 + 取类别 / 约束名）在根包再导出，见 R4。

**验收**：

- 唯一索引冲突 → `errors.Is(err, dbwm.ErrUnique)` 为真；
- 类型不匹配 → `errors.Is(err, dbwm.ErrTypeMismatch)` 为真；
- 框架自身的错误（渲染期参数错误、模型解析错误）→ `dbwm.KindOf` 返回 `KindUnknown`，且 `errors.Is(err, dbwm.ErrDatabase)` 为假；
- `errors.As` 能从包装后的错误里取到驱动原始错误；
- 错误信息里同时出现类别中文名与出错语句。

### R2 类别识别（驱动差异归方言）

**需求**：识别规则按驱动走，dbwm 主体不认识具体驱动。

**规则**：

1. 识别发生在执行路径上：驱动错误被包装成 `*dbwm.Error` 时分类；
2. **方言可选扩展** `ErrorClassifier`：`ClassifyError(err error) (Kind, bool)`。方言知道自己的驱动怎么报约束错误（SQLite 的错误码、MySQL 的 errno、PostgreSQL 的 SQLSTATE）；`ok` 为 false 表示认不出，交给通用兜底；
3. `Dialect` 接口**不加**这个方法 —— 自定义方言不实现也能编译，只是识别精度落到通用兜底；
4. 三套内置方言的识别规则：

   | 方言 | 识别依据 | 归类 |
   | --- | --- | --- |
   | ANSI（SQLite） | 错误码 `19`（约束，看消息细分）、`20`（类型不匹配）、`18`（值过大） | 类型不匹配 ← `20`/`18`；其余按消息细分 |
   | MySQL | errno（从 `Error <n> (SQLSTATE): …` 取）`1062` `1586` `1169` / `1048` `1364` / `1451` `1452` `1216` `1217` / `3819` `4025` / `1366` `1264` `1406` `1292` `1690` | 唯一 / 非空 / 外键 / CHECK / 类型不匹配 |
   | PostgreSQL | SQLSTATE `23505` / `23502` / `23503` / `23514` / `22P02` `22001` `22007` `42804` `22032` | 唯一 / 非空 / 外键 / CHECK / 类型不匹配 |

5. **通用兜底**按错误文本特征判定（不依赖任何驱动的结构化信息），覆盖三套内置方言的常见消息。特征是**包含式子串**（`unique constraint` 同时吃下 SQLite 的 `UNIQUE constraint failed:`、PostgreSQL 的 `violates unique constraint` 与 modernc 驱动的 `constraint failed: UNIQUE constraint failed:` 前缀形态）：

   | 文本特征（包含式子串，不区分大小写） | 类别 |
   | --- | --- |
   | `unique constraint`、`duplicate entry`、`duplicate key value` | 唯一冲突 |
   | `not null constraint`、`not-null constraint`、`cannot be null` | 非空约束 |
   | `foreign key constraint` | 外键约束 |
   | `check constraint` | CHECK 约束 |
   | `datatype mismatch`、`cannot store`、`string or blob too big`、`incorrect string value`、`out of range value`、`data too long`、`value too long for type`、`invalid input syntax for type`、`invalid text representation`、`string data right truncation` | 类型与值不匹配 |

6. 识别顺序：哨兵（错误链里已带类别哨兵）→ 方言识别 → 通用兜底；都不中则 `KindOther`（**不是** `KindUnknown` —— `KindUnknown` 留给"不是数据库错误"）；
7. 约束名提取尽力而为，认不出留空：

   | 形态 | 提取结果 |
   | --- | --- |
   | SQLite `… constraint failed: person.email` | `person.email` |
   | MySQL `… for key 'person.uk_email'` | `person.uk_email` |
   | PostgreSQL `… violates unique constraint "person_email_key"` | `person_email_key` |

**验收**：

- 内存 SQLite 插入重复主键 → 类别 `KindUnique`；
- 内存 SQLite 往 NOT NULL 列写 NULL → 类别 `KindNotNull`；
- 伪造 MySQL 错误文本 `Error 1062 (23000): Duplicate entry 'x' for key 'person.uk_email'` → 类别 `KindUnique`，约束名 `person.uk_email`；
- 伪造 PostgreSQL 错误（实现 `SQLState() string` 返回 `23505`）→ 类别 `KindUnique`；
- 认不出的驱动错误 → 类别 `KindOther`，且 `errors.Is(err, dbwm.ErrDatabase)` 为真。

### R3 其他错误（不是约束冲突的数据库错误）

**需求**：业务侧能分清"数据库报的错"与"框架自己报的错" —— 前者多半是服务端问题，后者多半是调用方把参数写错了。

**规则**：

1. 认不出类别的驱动错误归 `KindOther`，`errors.Is(err, dbwm.ErrDatabase)` 仍为真；
2. 框架自身的错误（渲染期参数错误、模型解析错误、条件为空拒绝全表更新、事务已结束…）**不是**数据库错误：`ErrorKind` 返回 `KindUnknown`，`errors.Is(err, dbwm.ErrDatabase)` 为假；
3. 事务开启 / 提交失败按驱动错误分类，`Action` 分别记"开启事务" / "提交事务"（提交可能撞上延迟约束冲突，类别有意义）；
4. 脚本执行失败（`ExecScript`）同样分类，但**错误信息里不带脚本正文**（脚本可能极长，定位信息由上层带文件名补上）—— 见 prd-dbwm R11.4；
5. 错误不吞、不改写：底层驱动错误原样保留在正文里，并可 `errors.As` 穿透。

**验收**：

- dbwm 渲染期报错 → `ErrorKind` 为 `KindUnknown`；
- 连接中断导致的执行错误 → `KindOther` 且 `errors.Is(err, dbwm.ErrDatabase)` 为真；
- `ExecScript` 失败 → 类别可判定，错误信息里不含脚本正文。

### R4 业务侧判定与处理

**需求**：给业务侧一套"判定 + 取信息"的接口，不把 HTTP / 重试策略塞进框架。

**规则**：

1. 根包 `slyorm` 暴露判定接口，语义与 `dbwm` 一致：`IsUnique` / `IsTypeMismatch` / `IsNotNull` / `IsForeignKey` / `IsCheck` / `ErrorKind` / `ConstraintOf`；类别常量在根包再导出（`Kind` 是 `dbwm.Kind` 的类型别名，与 `util.Range` 的再导出同款）；
2. 业务侧的典型处理（框架不提供映射，由业务自己定）：

   ```go
   err := users.Create(ctx, u)
   switch {
   case slyorm.IsUnique(err):
       // 唯一索引冲突 → 提示"该记录已存在"
   case slyorm.IsTypeMismatch(err), slyorm.IsNotNull(err):
       // 值放不进列 → 参数错误
   default:
       // 其余按内部错误处理
   }
   ```

3. `ConstraintOf` 给出"哪个索引 / 哪一列冲突"，认不出时返回空串；
4. 业务侧不需要 `*dbwm.Error` 也能判定（谓词接口）；需要细节时用 `errors.As` 取 `*dbwm.Error`。

**验收**：

- `slyorm.IsUnique` 与 `errors.Is(err, dbwm.ErrUnique)` 判定一致；
- `slyorm.ErrorKind` 返回的类别与 `dbwm.KindOf` 一致；
- `slyorm.ConstraintOf` 返回唯一索引名 / 冲突列。

### R5 值放不进列的错误归类

**需求**：值放不进列（写侧序列化不出、读侧转换不过）与数据库报的类型不匹配是同一类问题，业务侧应当用同一个判定。

**规则**：

1. 写侧 json 列序列化失败 → 错误带上 `dbwm.ErrTypeMismatch` 哨兵（值放不进 json 列）；
2. 读侧扫描目标（NULL 兜底、时间兼容、JSON 反序列化）转换失败 → 错误带上 `dbwm.ErrTypeMismatch` 哨兵；
3. 读侧错误经 dbwm 的执行路径包装成 `*dbwm.Error`，类别因此是 `KindTypeMismatch`；
4. 错误信息仍以各自的包名开头（`insert: ` / `rows: `），位置信息（源类型、目标类型）留在原文里；
5. **只带哨兵的错误链不算数据库错误**：写侧序列化失败发生在值到数据库之前，`KindOf` 照常给出类别（谓词可判定），但 `errors.Is(err, dbwm.ErrDatabase)` 为**假** —— 读侧经 `*dbwm.Error` 包装（真碰了数据库）才命中 `ErrDatabase`。

**验收**：

- json 列的值序列化不出 → 类别 `KindTypeMismatch`，且 `errors.Is(err, dbwm.ErrDatabase)` 为假（没到数据库）；
- 文本列的值读不进数值字段 → 类别 `KindTypeMismatch`，错误信息里能看到源类型与目标类型。

### R6 查询未命中不是错误

**需求**：查询没有获得记录是正常结果，框架不做错误处理 —— 业务侧不该因为"查不到"而走进错误分支。

**规则**：

1. 查询未命中**不是数据库错误**：不进类别集合，`ErrorKind` 返回 `KindUnknown`，`errors.Is(err, dbwm.ErrDatabase)` 为假；
2. 多行查询未命中 → 返回空结果，不是错误（dbwm 的 `Qryall` 不报错，门面 `Query` 返回空切片）；
3. 单行查询未命中 → dbwm 层用哨兵 `dbwm.ErrNoRows` 表示（中间层信号，**不是类别**），读侧与门面把它转成空结果：门面 `Get` 返回 `(nil, nil)`（见 prd-slyorm R3.2）；
4. 业务侧按空结果处理（判 nil / 判长度），不按错误处理；框架不替业务决定"未命中 → 404"这类映射（见 §1.3 第 2 条）；
5. **表 / 列不存在不在本条豁免范围内** —— 那是 schema 问题，属数据库错误，归 `KindOther`。

**验收**：

- 条件查不到记录 → 门面 `Get` 返回 `(nil, nil)`，err 为 nil；
- 多行查询查不到记录 → 返回空切片，err 为 nil；
- dbwm `Qryone` 未命中 → `errors.Is(err, dbwm.ErrNoRows)` 为真，但该错误不属于任何类别（`KindOf` 为 `KindUnknown`，`errors.Is(err, ErrDatabase)` 为假）。

---

## 5. API 一览

### 5.1 `dbwm`

| 符号 | 说明 |
| --- | --- |
| `type Kind int` | 错误类别 |
| `KindUnknown` `KindUnique` `KindTypeMismatch` `KindNotNull` `KindForeignKey` `KindCheck` `KindOther` | 类别常量 |
| `ErrUnique` `ErrTypeMismatch` `ErrNotNull` `ErrForeignKey` `ErrCheck` `ErrDatabase` | 类别哨兵错误 |
| `ErrNoRows` | 查询未命中的哨兵（**不属于类别集合**，见 R6） |
| `type Error struct` | 数据库执行错误：`Kind` / `Action` / `Constraint` / `Statement` / `Err` |
| `type ErrorClassifier interface` | 方言可选扩展：`ClassifyError(err error) (Kind, bool)` |
| `KindOf(err error) Kind` | 取错误类别 |
| `ConstraintOf(err error) string` | 取约束名 / 冲突列 |
| `IsUnique` `IsTypeMismatch` `IsNotNull` `IsForeignKey` `IsCheck` | 类别谓词 |

### 5.2 根包 `slyorm`

| 符号 | 说明 |
| --- | --- |
| `type Kind = dbwm.Kind` | 类别（别名） |
| `KindUnique` `KindTypeMismatch` `KindNotNull` `KindForeignKey` `KindCheck` `KindOther` `KindUnknown` | 类别常量（别名） |
| `ErrorKind(err error) Kind` | 取错误类别 |
| `ConstraintOf(err error) string` | 取约束名 / 冲突列 |
| `IsUnique` `IsTypeMismatch` `IsNotNull` `IsForeignKey` `IsCheck` | 类别谓词 |

---

## 6. 与既有包的关系

| 包 | 变化 |
| --- | --- |
| `dbwm` | 新增 `err.go`（类别 / 哨兵 / 错误类型 / 谓词）与 `errclass.go`（识别规则）；执行路径的错误包装改为 `*dbwm.Error`；方言实现 `ErrorClassifier` |
| `rows` | 扫描转换失败时带上 `dbwm.ErrTypeMismatch` 哨兵；查询未命中转译成空结果（R6） |
| `insert` | json 列序列化失败时带上 `dbwm.ErrTypeMismatch` 哨兵 |
| 根包 `slyorm` | 新增 `error.go`：类别别名与谓词接口 |

---

## 7. 已知限制

1. **模型侧不校验字段类型** —— 模型没有列类型信息，写入时值与列类型是否匹配由数据库判定；SQLite 动态类型下多数类型不匹配不会报错（存下即可），只有 STRICT 表与部分表达式会报；
2. **约束名提取是尽力而为** —— 靠错误文本里的形态识别，换了驱动或本地化过的消息就可能取不到（取不到时 `ConstraintOf` 返回空串，不影响类别判定）；
3. **MySQL 的 errno 从错误文本里解析** —— 本库不 import 驱动，拿不到 `MySQLError.Number` 字段，只能解析 `Error <n> …` 前缀；
4. **只覆盖约束类与类型类的常见错误** —— 连接失败、超时、死锁等归 `KindOther`，业务按内部错误处理即可；
5. **PG / MySQL 未做真库验证** —— 当前验证以内存 SQLite + 伪造驱动错误为主（与 prd-dbwm §8 一致）。

---

## 8. 已拍板

1. **错误类型放 `dbwm`，不另起包** —— 错误在执行路径上产生，与执行层同包最直接；业务侧接口在根包再导出（与 `util.Range` 的再导出同款）；
2. **类别用哨兵 + 谓词两套入口** —— 哨兵给 `errors.Is` 用，谓词给不想 import 中间层的业务用；
3. **方言实现识别是可选扩展** —— `Dialect` 接口不加方法，自定义方言不实现也不影响编译；
4. **不做 HTTP 映射、不做重试** —— 框架不认识 Web 层，重试策略属于业务；
5. **查询未命中不做错误处理** —— 查不到记录是正常结果：门面给 `(nil, nil)`，不进类别集合（见 R6）。

---

## 9. 实现参考

| 文件 | 内容 |
| --- | --- |
| `dbwm/err.go` | `Kind` 与类别常量、哨兵错误、`*Error`、谓词与取信息的接口 |
| `dbwm/errclass.go` | `ErrorClassifier`、识别顺序、方言识别规则与通用兜底、约束名提取 |
| `dbwm/dialect.go` | 三套内置方言的 `ClassifyError` 实现（转发到 errclass.go 的规则表） |
| `dbwm/sql.go` | `execFail` 移入 err.go，改为构造 `*dbwm.Error` |
| `dbwm/exec.go` `dbwm/querier.go` `dbwm/tx.go` `dbwm/exec_script.go` | 错误包装改为 `*dbwm.Error` |
| `select/scan.go` | 扫描转换失败时挂 `dbwm.ErrTypeMismatch` |
| `select/select.go` | `QueryOne` 把 `dbwm.ErrNoRows` 转成 `(nil, nil)`（R6） |
| `insert/value.go` | json 序列化失败时挂 `dbwm.ErrTypeMismatch` |
| `error.go`（根包） | 类别别名与业务侧判定接口 |
