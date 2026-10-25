# 项目规范

## 项目概览

`slyorm` 是一个 Go 写的 ORM 库（module `slyorm`，Go 1.27）。核心思路：用**结构体 + `dbx` 标签**声明式描述一张表的读写语义，运行时用反射生成 SQL，调用方不写手写 SQL。

- **文档先行**：每个能力先有 PRD（`docs/prd-*.md`），PRD 里编号的需求条目（R1、R2…）是实现的依据；代码注释用编号互引（如"见 PRD R3 ⑧"）。
- **按能力分包**：一个能力一个包，放在模块根下（`model/`、`select/`、`query/`、`insert/`、`dbwm/`、`migration/`、`util/`）；业务门面在模块根包 `slyorm`（`Collection` 表访问器）。
- **当前进度**：已落地 dbwm（SQL 渲染与执行的中间层）、query（条件结构体 → `*dbwm.Wnode`）、insert（写规则 → `*dbwm.Insert`/`Update` IR）、model（列名推导唯一入口）、select/（包名 `rows`，行模型投影与扫描）、migration/（按水位执行业务 SQL 脚本目录并留痕）与门面 slyorm（见 `docs/prd-slyorm.md`、`docs/prd-migration.md`）。**不从模型生成 DDL**：建表 / 初始化脚本由业务手写 SQL，经 `db.Migrate` 执行。

## 规范索引

`.claude/` 下是跨会话的规则与实操手册，与本文的项目专属约定各管一域；本文只写与本项目绑定的部分：

| 文件 | 管什么 |
|---|---|
| [rules/chat.md](rules/chat.md) | 一条指令的语义判定：这轮只答，还是动手 |
| [rules/collab.md](rules/collab.md) | 协作流程：给选项还是执行、不可行时怎么中断 |
| [rules/code.md](rules/code.md) | 编码纪律：分层、依赖注入、错误处理、Go 写法 |
| [rules/note.md](rules/note.md) | 注释规范：写什么、写多长、不写什么 |
| [rules/doc.md](rules/doc.md) | `.claude/` 下文档的建、改、废 |
| [skills/code-name/SKILL.md](skills/code-name/SKILL.md) | 命名规范：标识符、表字段、索引、HTTP 接口 |

## 1. 总体原则

项目处于开发设计阶段，不需要考虑功能兼容，也不需要设计兼容代码，按最简洁合理的方式组织代码。

- 不写兼容层、弃用别名、双路径实现；旧设计可以直接改掉。
- 不为"以后可能用得上"提前抽象、预留钩子。
- **简洁 ≠ 简陋**：错误信息要能定位问题，边界行为要能被测试覆盖；该有的扩展点（如注册表）放在明确位置，不散落各处。

## 2. 目录与文件组织

一个包一个能力，包名 = 目录名 = 能力名（`insert/` → `package insert`）。唯一例外：行模型读取在 `select/` 目录、包名 `rows`（`select` 是 Go 关键字，不能作包名；与 `query` 包的先例同款）。**按关注点分文件**，参考 `insert/`：

| 文件 | 放什么 |
| --- | --- |
| `insert.go` | 对外 API（`DB`、入口方法），包注释与文件分工说明放在这里 |
| `exec.go` | 入参归一、切片处理 |
| `model.go` | 写侧模型与列的元信息、表名推导 |
| `parse.go` | 静态列 → 写侧列（选项校验、defv 字面量解析） |
| `option.go` | 写规则的选项名与自定义选项注册表 |
| `value.go` | 取值与写回 |
| `default.go` | 默认值（函数与字面量）注册表 |
| `plan.go` | 写入计划（值成形）与语句渲染 |
| `*_test.go` | 同包白盒测试 |

模块根下的包（`dml/` 层级已取消）：

| 包 | 管什么 |
| --- | --- |
| `slyorm`（根包） | 业务门面：`DB` 句柄、`Collection`、分页协议、通用查询类型、`RegisterUser` 审计列 |
| `model/` | 列名推导的**唯一入口**：静态列集合与主键标记，insert 与 select 共用 |
| `select/`（`rows`） | 行模型投影计划、RowFactory、扫描目标（NULL 兜底 / 时间兼容 / JSON） |
| `query/` | 读规则：条件结构体 → `*dbwm.Wnode`（值成形：like 补 %、区间展开） |
| `insert/` | 写规则：模型 → `*dbwm.Insert`/`Update` IR 与执行入口 |
| `migration/` | 数据迁移：按水位执行业务 SQL 脚本目录并留痕（`sys_migration`，见 `docs/prd-migration.md`） |
| `dbwm/` | SQL 渲染与执行、方言、事务（连接接口归它所有） |
| `util/` | 共用工具：标签文本处理、取值与零值判定、`Range[T]`（方言已归 `dbwm/`） |

- **对外 API 只放在 `<能力>.go`**，且每个方法保持短小：方法的正文只写"准备 → 计划 → 执行"这样的骨架，机制下沉到对应文件。
- 文件内用分隔线（`// ---`）分节，节标题写清楚这块负责什么。
- 不要按"代码有几百行"来拆文件，按**关注点**拆；也不要为了凑文件把不相关的东西塞进同一个文件。
- 一个包只放一个能力，不混放。

## 3. 代码风格

- **注释与文档用中文**。导出符号必须有文档注释，且以符号名开头（`// Insert 插入一条记录。`）。
- 长文件用分隔线分段，段落上方写小标题：

  ```go
  // ---------------------------------------------------------------------------
  // 默认值
  // ---------------------------------------------------------------------------
  ```

- **错误**：`fmt.Errorf("insert: <中文说明>: %w", err)` —— 前缀是包名，说明用中文，包装底层错误一律用 `%w`。错误信息要指出**哪个字段 / 哪条规则**不满足，不要只写"参数错误"。
- 注册表破坏不变量的调用用 `panic`：非法入参（nil / 空名）、重名、覆盖内置 —— 注册发生在 init 期，返回 error 必被调用方忽略；运行期错误用 `error` 返回（口径见 [rules/code.md](rules/code.md) §4.3、§5.2）。
- **反射细节集中封装**（`util.IsZero`、`util.Value`、`bindValue`、`setValue`…），不在业务流程里散落 `reflect` 操作。
- 模型解析结果按类型缓存（`modelCache`），不要每次调用都重新解析标签。

## 4. 依赖与数据库访问

- **连接接口归 dbwm 所有**（`Conn`/`Execer`/`Querier`/`Tx`，`deps.go` 里 `sqlConn`/`sqlTx` 适配）；其余能力包与门面不直接碰 `database/sql`，只引用 `dbwm` 的类型与 IR。
- **驱动由调用方注册**：本库不 import 任何驱动 —— `Open` 只是按名字 `sql.Open(driver, dsn)`，驱动要在调用方引入（`_ "modernc.org/sqlite"`），测试文件自己注册。新增数据库支持时只加方言实现，不改业务路径。
- **注入入口**：`dbwm.New(conn, opts...)` 是主要入口，`Open(driver, dsn, opts...)`/`NewSQL(base, opts...)` 是便利构造（insert / slyorm 门面同款转发）；**方言是构造选项**（`dbwm.WithDialect`），不给选项时用 ANSI。
- **连接只以接口形态出现**：不提供"取回底层 *sql.DB"的逃生舱 —— 注入 *sql.DB 的一方自己留着句柄即可（要自己执行建表 SQL 的用 `NewSQL` 形态）。
- **事务用 ctx 传递**：`db.Tx(ctx, fn)` 里 `fn` 收到的 ctx 自动带上事务，嵌套调用复用外层事务，由最外层提交或回滚。

## 4.1 数据库方言

- **核心 SQL 是标准 SQL**：业务规则（列清单、零值语义、默认值、自增回填）与数据库无关。
- **方言只管标准 SQL 覆盖不到的少数几点**（见 `dbwm/dialect.go` 的 `Dialect` 接口）：标识符引用、占位符、分页写法、取回自增值的方式、参数上限。
- 新增数据库支持 = 新增一个方言实现，**业务代码不动**；不要把方言判断散进业务逻辑。
- `Open` 传 nil 方言时**自动识别**库型（探测 `version()` / `sqlite_version()`）；识别不出来就报错，由调用方显式指定 —— 不猜。

## 5. `dbx` 标签约定

- 选项全集见 `docs/prd-insert.md`（写规则）与 `docs/prd-select.md`（读规则，即查询条件）；写规则专属：`null` `autopk` `deff` `defv`；读写通用：`col` `json` `inline`。
- **未知选项忽略但保留**到 `Column.Opts`（不报错、不阻断写入）—— DDL 侧、查询侧的选项不该污染写包。
- 选项名统一转小写；选项值按给定保留，只脱外层引号（成对引号才成组）。
- 重复选项、语义冲突（`deff`+`defv`、`json`+`inline`、`null`+`autopk`）在**解析期**报错。
- 表名**不能**写在标签里（reflect 读不到类型自身的 tag）：insert 独立使用时按 `TableName()` 方法 > `RegisterTable` > 类型名 snake_case 推导；门面 `Collection` 的表名**构造期显式传入**（同一模型可映射多张表）。
- 主键标记：`autopk`（自增主键，隐含主键）、裸 `pk`（非自增 / 复合主键，供 Update 剔除 SET）；`pk=组名` 的组名不认。
- 扩展点集中在注册表：`RegisterOption`（自定义选项，insert / query）、`RegisterDefault`（默认值函数，insert；重复注册 panic）、`RegisterTable`（表名，insert）、`RegisterOperator`（自定义算子，query 归约为核心算子 / dbwm）、`RegisterUser`（审计列当前操作用户，slyorm）。
- 注册必须在**该模型第一次被解析之前**完成 —— 模型元信息按类型缓存，之后再注册对已缓存的模型无效。
- 新增选项时同步更新 PRD 的选项总表与该包 `option.go` 里的选项名常量表。

## 6. 测试

- **同包白盒测试**（`package slyorm` / `model` / `rows` / `query` / `insert` / `migration` / `dbwm` / `util`）：可以直接测未导出的解析函数。
- **两种验证方式**：
  - 要验证真实 SQL 行为 → 内存 SQLite + 手写建表（自持 `*sql.DB` 建 DDL，`NewSQL` 注入句柄；`:memory:` 须 `SetMaxOpenConns(1)`），`t.Cleanup` 关库；
  - 要断言"执行了哪条 SQL" → 用 `dbwm` 测试里的 `fakeConn` 替身，不碰真实数据库。
- 断言信息用中文，写清"实际 vs 期望"：`t.Fatalf("SQL =\n%s\n期望\n%s", stmt, want)`。
- **一条规则一个顶层 `TestXxx`**，并按 PRD 编号分段（见 `insert_test.go` 的 `// R4 零值与 NULL` 这样的分节）；用例少时顺序断言，分支多时用 `t.Run` 子测试；辅助函数用 `t.Helper()`。
- 每条需求条目（R1…）至少有一个测试；测试名体现规则本身（`TestNullOnZero`、`TestAutoPKBackFilled`）。
- 测试模型（`person`、`plain`…）放在测试文件顶部，用小写（不导出），避免与真实模型混淆。

## 7. 工作流与提交

- 改行为按 **PRD → 实现 → 测试** 的顺序：先改 `docs/prd-*.md`，再改代码，最后补/改测试。
- 提交信息用中文，说明改了哪条规则/行为；一次提交只做一件事。
- 不随意新增依赖；`go.mod` 只放直接依赖，用 `go mod tidy` 保持整洁。
- 提交前跑 `go vet ./...` 和 `go test ./...`。
- 提交前自检（在仓库根执行；检测 grep 的通用门槛见 [rules/doc.md](rules/doc.md) §五）：
  - 能力层不 import 根包与同层兄弟：`grep -rnE '"slyorm"|"slyorm/(insert|query|select|migration)"' --include=*.go --exclude=*_test.go model/ query/ select/ insert/ migration/ dbwm/ util/` —— 应无命中（根包 import 全部能力包，方向不可反；测试文件引用兄弟包做集成验证不算违规）；
  - 具体实现未泄漏：`grep -rnE "sql\.DB|sql\.Tx" --include=*.go --exclude=*_test.go model/ query/ select/ insert/ migration/ dbwm/ util/ slyorm.go collection.go collection_helper.go list.go types.go audit.go migrate.go 2>/dev/null | grep -v "[/][/]"` —— 逐条核对，只允许 `dbwm/deps.go` 的适配器内嵌字段与各 `NewSQL` 形参（`dbwm/`、`insert/`、根包 `slyorm.go`），其余命中即违规（注释过滤必须写 `[/][/]`，写成 `//` 在 ugrep 下会吞掉全部输出）；
  - 错误格式统一：`grep -rnE "fmt\.Errorf\(|errors\.New\(" --include=*.go --exclude=*_test.go model/ query/ select/ insert/ migration/ dbwm/ util/ *.go | grep -vE "(insert|query|dbwm|util|model|rows|slyorm|migration):|Errorf\(\s*$|New\(\s*$"` —— 输出即未带包名前缀的错误（多行 Errorf 的前缀在续行，已被行尾过滤）；前缀白名单随新增能力包同步，存量违规逐步清零，新代码 0 命中。

## 8. 明确不做

- 不写兼容层、弃用标记、feature flag。
- 不引入第三方 ORM / 查询构建库 —— 语义由本项目自己的 PRD 定义。
- 不从模型生成 DDL、不做表结构推导 —— 建表 / 初始化 / 结构修补由业务手写 SQL 编排成脚本目录，`migration/` 按水位执行（见 `docs/prd-migration.md`）；新能力先写 PRD。
- 不把驱动、连接池、事务实现泄漏进业务逻辑。
