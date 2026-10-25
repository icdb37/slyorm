---
name: code-name
description: Use when modifying code: code variable/constant/function/structure naming conventions, code comment conventions, database table field and index naming conventions, HTTP interface naming conventions
---

# 命名规范

代码修改时命名规范包括但不限于以下方面：
* 代码: 代码变量/常量/函数/结构体命名规范，代码注释规范
* 接口: HTTP接口路径常量、路径参数、请求参数、body中字段名、接口数据的枚举值
* 数据库: 表名、索引名、表字段名

注释规范的完整条文以 [rules/note.md](../../rules/note.md) 为准，本文件只落项目基线，不重复规则本体。

## 专有名词

- **kebab-case**: 字母小写，数字，单词之间用连字符（-）连接，例如: `user-id`
  * http接口: 路径常量（如 `/api/v1/auths/refresh-token`）
- **snake_case**: 字母小写，数字，单词之间用下划线连接，例如: `user_id`
  * http接口: 路径参数、请求参数、body中字段名（json 标签）
  * db规范: 表名、索引名、表字段名（dbx 列名）
  * config配置: 配置文件（yaml）配置项键名（如 `jwt_secret`）
- **SNAKE_CASE**: 字母大写，数字，单词之间用下划线连接，例如: `USER_ID`
  * http接口： 接口数据的枚举值（由 enumer `-transform=snake-upper` 生成，如 `"ENABLED"`）
- **dot.case**: 单词之间用点号（.）连接，通常用于域名、命名空间，例如: `com.app.slyetr`
- **PascalCase**: 每个单词首字母都大写，例如: `UserID`
  * go代码: 导出函数名、导出变量、导出常量、导出结构体等
- **camelCase**: 首字母小写，后续每个单词首字母大写，例如: `userID`
  * go代码: 包内部函数名、包内部变量、函数局部变量、结构体内部变量等

## 编码规范

### Go 代码

- 初始词大写约定：`.*ID`（不用 `Id`）、`.*UUID`（不用 `Uuid`）、`.*URL`（不用 `Url`）、`.*HTTP`（不用 `Http`）
- 基础设施包命名：`internal/infra` 下统一「功能域缩写 + x」——`csx`（上下文）、`erx`（业务错误）、`ehx`（HTTP 框架）、`lgx`（日志）、`upx`（口令哈希）、`cvx`（校验器）、`dbx`（数据访问）、`mgx`（数据迁移）、`fsx`（文件存储）；新增基础设施包沿用该风格
- 模型分层命名：`model/vo`（接口层视图）、`model/bo`（业务对象，兼任表模型）、`shared/model/cco`（三层公用数据结构）；转换函数 `ToBO` / `XxxFromBO`

### 标识符长度

- 默认取**能表意到的最短名字**：上下文（类型、参数、调用点）已经说清的事，不再写回名字里
- 作用域越小、上下文越强，可以越短：局部变量与循环变量用 `i` `err` `row`，包内变量用 `cols` `opts`
- 缩写只用于**通用或已约定**的词（`ctx` `cfg` `tx` `db` `sql` `col`）；自造缩写必须一眼能读出来
- 不为"将来也许会重名"提前加限定词：`User.Name` 里的 `User` 是冗余
- 短名不豁免文档注释：导出标识符仍须有一行以标识符名开头的文档注释，名字省下的上下文由它补上

| 位置 | 上下文由谁提供 | 建议 |
|---|---|---|
| 包级导出符号 | 无 —— 脱离包名后要独立成立 | 名字自带语义：`InsertAll` |
| 包内函数与变量 | 包名 + 所在文件 | 去掉冗余限定：`insert.lookupOption` |
| 结构体字段 | 结构体类型名 | 只说"是什么"：`User.Name`（不是 `User.UserName`） |
| 接口方法 | 接口名 | 只说"做什么"：`Conn.BeginTx`（不是 `Conn.BeginTransactionOnConn`） |
| 局部变量 / 循环变量 | 所在语句 | 尽量短：`i` `err` `row` |

业务上下文已经点名时，标识符不必再带一遍业务域。

✅ 正例：包名已说明是 users 领域，类型与方法名各说各的事。

```go
package users

// Store 是 users 领域的存储。
type Store struct{ ... }

// Find 按 id 取用户。
func (s *Store) Find(ctx context.Context, id string) (*User, error) {
	row, err := s.conn.QueryRowContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("users: 查询用户 %s 失败: %w", id, err)
	}
	return scanUser(row)
}
```

❌ 反例：领域词在包名、类型名、方法名、参数名里各说一遍。

```go
func (s *UserStore) FindUserByUserID(ctx context.Context, userID string) (*User, error)
```

### 枚举命名规范

- 枚举类型定义为 `int`（iota），不用字符串类型；JSON 表示与 `String()` 由 `enumer` 生成（SNAKE_CASE）
- 常量命名 PascalCase，格式 `<枚举类型><枚举值>`；零值统一定义为 `<枚举类型>Undefined`
- 枚举列落库为 INTEGER（dbx 类型段 `int=N` 指定合法默认值）

定义骨架、生成流水线与演进纪律见 [code-model](../code-model/SKILL.md)。

### 注释基线

条文见 [rules/note.md](../../rules/note.md)（写「为什么」、函数头注释以标识符名开头、导出标识符必须有文档注释、禁模板式多行注释）。项目示例：

```go
// Package bo users 特性的业务对象：service/store 两层共用的数据结构，
// 兼任持久化模型（dbx 标签），不携带接口层 json 标签。
package bo

// User 用户业务对象：全行数据，Password 为库中 argon2id 哈希，明文不出 service 层。
type User struct {
	ID       int    `dbx:"id,pk,autoinc"`
	Name     string `dbx:"name,,unique=users_unique_name"`
	Status   Status `dbx:"status,int=1"`
}
```

## 数据库规范

使用 SQLite（modernc.org/sqlite）作为业务数据存储；表结构由 model/bo 表模型的 `dbx` 标签声明（见 [code-dbx](../code-dbx/SKILL.md)），不在代码里手写 DDL。

### 命名规范

- 表名 **snake_case 复数**（如 `users`），定义在各特性 `model/bo` 的 `Table<Feature>` 常量中
- 字段名符合 **snake_case** 规范
  - 时间类型字段格式为过去分词+`_at`，例如: `created_at`, `updated_at`, `published_at`等
  - 依赖其他表业务数据关联字段为 `<feature>_id`，例如: `user_id`, `task_id`等
  - 列表/集合字段使用名称复数
- 索引名符合 **snake_case** 规范
  - dbx 未显式命名时自动合成 `uk_<列名>`（唯一）/ `idx_<列名>`（普通）
  - 复合索引须多列显式同名（如 `unique=uk_tenant`），否则会被拆成单列索引
