# 编码规范

跨层硬约束：**代码怎么分层、模块之间怎么依赖、错误怎么报、Go 代码怎么写**。

本文件是编码纪律的权威层，**不含项目专属内容，可直接跨项目复用**；目录、文件命名、测试替身、自检命令的落地由各项目 CLAUDE.md 维护。相邻文档各管一域，本文件不重复它们的条文：

| 域 | 归属 | 位置 |
|---|---|---|
| 注释怎么写 | note.md | [note.md](note.md) |
| 标识符怎么命名 | code-name skill | [../skills/code-name/SKILL.md](../skills/code-name/SKILL.md) |
| 交互与实现流程 | collab.md / chat.md | [collab.md](collab.md)、[chat.md](chat.md) |
| 项目专属约定（目录、文件命名、测试、自检命令落地） | CLAUDE.md | [../CLAUDE.md](../CLAUDE.md) |

**冲突时**：注释以 note.md 为准，命名以 code-name 为准，协作流程以 collab.md 为准，项目专属部分以 CLAUDE.md 为准，其余以本文件为准。

---

## 一、总则

| 原则 | 落地要求 | 自检问题 |
|---|---|---|
| 分层清晰 | 依赖单向、无环，同层包互不 import | 上层换掉下层实现，要改几处？ |
| 单一职责 | 一个包一个能力，一个文件一个关注点 | 这个包名能概括它全部导出符号吗？ |
| 依赖接口 | 上层依赖接口，不依赖实现 | 实现细节有没有漏进上层？ |
| 逻辑简单 | 优先最直白的那条路径 | 删掉这层抽象，代码会变复杂吗？ |
| 可读可测 | 命名与结构自解释，行为可被断言 | 新人能在十分钟内定位入口吗？ |

取舍顺序：**正确 > 简单 > 可扩展 > 性能**。相邻两项冲突时选靠前的；性能只在被实测证明是瓶颈时才换。

---

## 二、分层与包边界

### 2.1 依赖方向

依赖只能自上而下，同层之间不横向依赖：

```
装配层（main / 上层包装）
        │  依赖
        ▼
能力层（若干能力包，互不 import）
        │  依赖
        ▼
工具层（共用机制）
```

| ❌ 禁止 | ✅ 要求 |
|---|---|
| 能力层之间互相 import | 共同的需求下沉到工具层，或由上层组装 |
| 工具层 import 能力层 | 语义留在拥有它的那层，工具层只提供机制 |
| 出现循环依赖 | 循环 = 边界画错了：抽共同概念或调整归属，不是「先这样」 |
| 下层反向调用上层（如工具层回调业务钩子） | 用接口 + 注入把回调交给上层注册 |
| 能力层导出泛型 API，上层类型渗进来 | 上层类型用回调 / 接口交付，泛型薄包装放装配层 |

### 2.2 一个包一个能力

- 包名 = 目录名 = 能力名；导出面 = 能力面。
- **不导出内部细节**：内部类型保持不导出，让调用方只经能力入口使用。
- 需要在包外做类型判断（如树形结构的节点）时，用**密封接口**（含不导出的标记方法），让「第三种实现」在类型上不可表达：

```go
// Node 表达式树的一个节点：叶子是 *Leaf，组是 *Branch。
//
// isNode 不导出 —— 接口因此密封：包外造不出第三种节点。
type Node interface {
	isNode()
}
```

### 2.3 什么才该下沉到工具层

| 该下沉 | 不该下沉 |
|---|---|
| 两处以上共用的**机制**（文本切分、零值判定、标识符引用） | 只在一处用的**语义**（某条业务规则的解释） |
| 与具体业务无关的通用能力 | 「某张表怎么写」这种业务规则 |
| 有独立测试价值的纯函数 | 需要知道当前业务上下文才成立的逻辑 |

判据：下沉后，工具层是否仍然**不认识业务**？认识业务的就不该下沉。

---

## 三、文件组织

- **按关注点拆，不按行数拆**：文件大不是拆的理由，职责杂才是；也不要为凑文件把不相关的东西塞进同一个文件。
- **对外 API 集中在包名同名的入口文件**：方法正文只写「准备 → 计划 → 执行」这样的骨架，机制下沉到对应文件；方法超过一屏就该拆。
- **包注释写在与包同名的文件顶部**，含三段：职责一句话、典型用法示例、文件分工表。
- **文件内用分隔线分节**，节标题写清这块负责什么：

```go
// ---------------------------------------------------------------------------
// 依赖抽象
//
// 本包不直接依赖 *sql.DB：对数据库的调用一律走下面的接口。
// ---------------------------------------------------------------------------
```

具体文件怎么命名（依赖、解析、执行各放哪）是项目专属约定，见各项目 CLAUDE.md。

---

## 四、依赖与注入

### 4.1 接口归使用方所有

需要外部能力时，**在本包定义接口**，而不是去第三方包找。接口按用途拆小，
再由组合接口（`Conn`）汇总 —— 一个接口一件事，需要哪个嵌哪个：

```go
// Execer 执行一条带参数的语句。
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier 执行查询。
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx 是事务：执行、查询、提交、回滚。
type Tx interface {
	Execer
	Querier
	Commit() error
	Rollback() error
}

// Conn 是本包依赖的连接 —— 依赖倒置的落点。
//
// 本包只按接口调用 Conn：默认实现是 *sql.DB（见 NewSQL），
// 想换连接池、注入 mock、断言执行了哪条 SQL，都只需换一个实现了 Conn 的类型。
type Conn interface {
	Execer
	Querier
	// BeginTx 开启一个事务。
	BeginTx(ctx context.Context) (Tx, error)
}
```

| 规则 | 说明 |
|---|---|
| 方法集最小 | 一个接口一件事：执行、查询、事务分开，需要哪个嵌哪个 |
| 参数用接口 | 形参写 `Conn` 而不是 `*sql.DB` |
| 返回接口而非实现 | 返回能力面（`SQL() Conn`），不返回具体类型（`SQL() *sql.DB` ❌） |
| 不开逃生舱 | 注入具体实现的一方自己留句柄；不提供「取回底层实现」的方法 |

**构造入口**：`New`（主，依赖注入）+ 便利构造（`Open` / `NewSQL` 一类）。可选项传 `nil` 必须有**明确语义**（取默认值、探测、显式报错），不靠猜。

### 4.2 具体实现只出现在适配器里

- `*sql.DB` / `*sql.Tx` 一类具体实现**集中在一个适配文件**；业务文件里不出现具体连接类型。
- **驱动由调用方注册**：库不 import 任何驱动，只按名字 `sql.Open(driver, dsn)`；驱动注册写在调用方与测试文件里。

```go
// sqlConn 把 *sql.DB 适配成 Conn：具体驱动只在这一层出现。
type sqlConn struct {
	*sql.DB
}

// BeginTx 开启事务，并把 *sql.Tx 适配成 Tx。
func (c sqlConn) BeginTx(ctx context.Context) (Tx, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return sqlTx{tx}, nil
}
```

### 4.3 全局注册表是唯一的全局可变状态

注册表（选项处理器、默认值函数、算子表）用「注册 + 查表」实现扩展点，不靠继承或反射扫描：

| 环节 | 规则 |
|---|---|
| 注册 | 破坏注册表不变量 → `panic`：入参非法（nil / 空名），以及重名、覆盖内置。注册发生在 init 期，返回 `error` 必然被调用方忽略、扩展点静默失效（stdlib `sql.Register` 同例） |
| 读取 | 加锁访问（`sync.Map` / `sync.RWMutex`），读写锁优先 |
| 时机 | 注册必须在对应元信息**第一次被解析或使用之前**完成（按类型缓存的东西，之后注册无效） |
| 归属 | 注册表与它的语义同包，不跨包共享 |

```go
// OptionHandler 处理一条自定义选项。
type OptionHandler func(key, value string) error

var (
	optionMu      sync.RWMutex
	customOptions = map[string]OptionHandler{}
)

// RegisterOption 注册自定义选项；非法入参与重名一律 panic。
//
// 重新注册同名选项会覆盖先前的处理器，扩展点的行为因此不可预期 —— 宁可让调用方
// 在启动时就崩，也不要等到运行期才发现选项生效的不是自己注册的那个。
func RegisterOption(key string, h OptionHandler) {
	if key == "" {
		panic("store: 选项名不能为空")
	}
	if h == nil {
		panic("store: 选项处理器不能为 nil")
	}
	optionMu.Lock()
	defer optionMu.Unlock()
	if _, dup := customOptions[key]; dup {
		panic("store: 选项 " + key + " 已注册")
	}
	customOptions[key] = h
}
```

---

## 五、错误处理

### 5.1 错误格式与信息

```go
fmt.Errorf("<包名>: <中文说明>: %w", err)
```

| 要素 | 要求 |
|---|---|
| 包名前缀 | 一眼看出是哪层报的 |
| 中文说明 | 指出**哪个字段 / 哪条规则 / 哪个参数**不满足 |
| 底层错误 | 有底层错误时一律用 `%w` 包装，保留 `errors.Is` / `errors.As` 穿透能力；没有就不硬凑 |

| ❌ 反例 | ✅ 正例 |
|---|---|
| `errors.New("参数错误")` | `fmt.Errorf("store: 用户 %q 不存在", name)` |
| `fmt.Errorf("保存失败")` | `fmt.Errorf("store: 保存用户 %s 失败: %w", id, err)` |
| `errors.New("db: query failed")` | `fmt.Errorf("store: 查询用户 %s 失败: %w", id, err)` |
| `fmt.Errorf("store: %w", err)` | `fmt.Errorf("store: 查询用户 %s 失败: %w", id, err)` |

### 5.2 何时 panic，何时 error

| 情形 | 处置 |
|---|---|
| 数据不满足业务规则（SQL 报错、查询描述非法、配置缺失）——改数据重试有意义 | 返回 `error` |
| API 契约破坏的编程错误：注册入参非法 / 重名覆盖、必传回调为 nil、密封接口的类型断言失败——只能改调用代码 | 显式判断后 `panic`，信息可定位；不留给空指针去自然爆 |
| 框架边界（如 `http.Handler`） | 允许 panic，但包内业务路径不靠 panic 传递错误 |

判据：改**数据**就能重试 → `error`；只能改**调用代码** → `panic`。

### 5.3 哨兵错误

对外需要让调用方判定的错误用哨兵，包装后仍可穿透：

```go
// ErrNotFound 表示查询没有命中记录；调用方用 errors.Is 判定。
var ErrNotFound = errors.New("store: 记录不存在")
```

- 对外导出用 `ErrXxx`；包内专用用小写 `errXxx`。
- 多个错误都要保留时用 `errors.Join`，不要互相覆盖：

```go
// 回滚失败也不吞掉业务错误：join 起来，两个分支都能被 errors.Is 命中。
if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
	return errors.Join(err, fmt.Errorf("store: 回滚事务失败: %w", rbErr))
}
return err
```

### 5.4 不吞错误

- `_ =` 只允许出现在**确实无解**的清理路径（如探测失败后关掉刚开的连接池），且旁注写明为什么可以忽略。
- 不把错误降级成 `false` / `0` 返回，除非函数语义本就是「有无」而非「成功失败」。
- 不用 `panic`/`recover` 代替错误返回（`http.Handler` 等框架边界除外）。

---

## 六、函数与数据结构

### 6.1 函数

| 规则 | 说明 |
|---|---|
| 一函数一件事 | 名字能概括正文；正文与名字不符就是拆分信号 |
| 标识符尽量短 | 上下文能说清的事不再写回名字；作用域越小可以越短 —— 见 [code-name skill](../skills/code-name/SKILL.md) |
| 早返回 | 先处理异常分支，减少嵌套层级 |
| 参数 ≤ 4 个（`ctx` 计入） | 导出 API 的硬约束；包内函数超出时先考虑收进结构体 |
| 返回值 ≤ 3 个 | 多个同类型返回值用结构体 |
| 导出符号有文档注释 | 见 [note.md](note.md) |
| 反射等机制调用集中封装 | 业务流程里不散落 `reflect` 操作 |

### 6.2 数据结构

- **零值可用**：不想让人依赖零值的类型，构造函数必须把状态补齐。
- **接收者一致**：同一类型的所有方法用同一种接收者名（惯例取类型名首字母），且不混用指针与值接收者。
- **不外泄内部可变状态**：返回内部 slice/map 前拷贝一份；调用方改了返回值不该影响内部。
- **不可变优于可变**：能只读就别给 setter。

```go
// Cache 按 key 缓存解析结果。
//
// 零值不可用：map 为 nil，构造函数必须补齐。
type Cache struct {
	mu sync.RWMutex
	m  map[string]any
}

// NewCache 构造一个空缓存。
func NewCache() *Cache {
	return &Cache{m: map[string]any{}}
}
```

### 6.3 设计模式怎么选

| 场景 | 选择 | 不要 |
|---|---|---|
| 多种可替换实现（连接池、方言、算法策略） | 小接口 + 依赖注入 | 抽象基类 / 虚函数体系 |
| 有限枚举的分支 | 策略表 / 映射表，把分支数据化 | 散落各处的 `switch` |
| 需要一份跨包共享的注册数据 | 注册表 + 锁 | 裸全局变量 |
| 流程骨架固定、步骤会变 | 组合（把步骤做成接口注入） | 继承 + override |

**反面判据**：同一个 `switch` 判断散在三个文件里，就是「差异没收敛」的信号 —— 差异应收进接口或策略表，业务路径不动。

---

## 七、Go 书写细节

| 项 | 要求 |
|---|---|
| 空接口 | 用 `any`，不用 `interface{}` |
| context | 作为第一个参数传递；不存进结构体字段；跨层传递靠 ctx 携带 |
| `defer` | 紧跟获取资源的语句，别攒到函数末尾 |
| 切片/映射 | 用 `slices`、`maps` 标准库，不手写遍历 |
| 循环变量 | Go 1.22+ 每轮新建，不再手写 `x := x` |
| 返回顺序 | `error` 放最后 |
| 互斥锁 | 有锁区间内不调用外部代码（回调、IO、未知实现） |
| SQL 值与标识符 | 值一律走绑定参数，不拼进语句文本；动态标识符必须经引用 / 白名单处理 |
| 哈希/序列化 | 不依赖 map 遍历顺序；需要稳定输出时显式排序 |

常见写法：ctx 首参、资源紧跟获取、`defer` 紧贴资源语句、早返回。

```go
// Save 把用户写进库里；失败时事务回滚。
func (s *Store) Save(ctx context.Context, u *User) error {
	tx, err := s.conn.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // 清理路径：提交后回滚返回 ErrTxDone，忽略无妨

	if _, err := tx.ExecContext(ctx, "INSERT INTO users(id,name) VALUES(?,?)", u.ID, u.Name); err != nil {
		return fmt.Errorf("store: 写入用户 %s 失败: %w", u.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交事务失败: %w", err)
	}
	return nil
}
```

---

## 八、并发

| 规则 | 说明 |
|---|---|
| 全局可变状态必须同步 | 注册表、缓存用 `sync.Map` 或 `RWMutex`；读写锁优先 |
| 缓存按类型/键缓存 | 反射解析等昂贵结果按类型缓存，别每次调用重新解析 |
| 句柄的并发语义要分清 | 如 `*sql.DB` 可并发，`*sql.Conn` 与事务句柄由单次使用方持有 |
| 不共享可变业务状态 | 跨 goroutine 传递的是值或不可变结构 |

反射解析这类昂贵结果按类型缓存，别每次调用重新解析：

```go
// modelCache 按类型缓存元信息：reflect.Type -> *Model。
var modelCache sync.Map

// ModelOf 返回类型的元信息，命中缓存直接返回。
func ModelOf(t reflect.Type) (*Model, error) {
	if v, ok := modelCache.Load(t); ok {
		return v.(*Model), nil
	}
	m, err := parseModel(t)
	if err != nil {
		return nil, err
	}
	modelCache.Store(t, m)
	return m, nil
}
```

注册表这种「少量写、频繁读」的表用 `sync.Map`；需要更细的读锁语义时用 `sync.RWMutex`。

---

## 九、测试

替身怎么搭、用例怎么组织是项目专属约定，见各项目 CLAUDE.md。通用纪律两条：

- 测试与被测代码**同包**（白盒），便于直接验证未导出的纯函数。
- 断言要写出「实际 vs 期望」，失败信息里能直接看出差在哪：

```go
t.Fatalf("SQL =\n%s\n期望\n%s", stmt, want)
```

要断言「执行了哪条 SQL」就用接口的替身实现，不碰真实数据库：

```go
// fakeConn 是 Conn 的替身：只记录执行过的语句，不碰真实数据库。
type fakeConn struct {
	stmts []string
}

func (c *fakeConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.stmts = append(c.stmts, query)
	return fakeResult{}, nil
}

// fakeResult 是 sql.Result 的替身。
type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }
```

---

## 十、反例速查

| 现象 | 判据 | 修法 |
|---|---|---|
| 业务文件里出现具体连接类型 | 具体实现漏进上层 | 收进适配文件，上层只见接口 |
| 工具层 import 了能力层 | 分层反向 | 语义归上层，工具层只留机制 |
| 两个能力包互相 import | 边界画错 | 抽共同概念到工具层 |
| 错误信息只有「失败」 | 不可定位 | 补包名前缀 + 字段/规则 + `%w` |
| 函数里散落 `reflect.Value` 调用 | 机制混进流程 | 机制封装成工具函数 |
| 同一分支判断出现在多个文件 | 差异没收敛 | 收进策略表 / 接口 |
| 导出接口带一堆调用方用不到的方法 | 接口过胖 | 按使用场景拆小接口 |
| 注册表裸全局变量 | 并发不安全 | 加锁或 `sync.Map` |

---

## 十一、提交前自检

| 检查 | 怎么做 |
|---|---|
| 静态检查与测试 | `go vet ./...` && `go test ./...` |
| 分层未被破坏 | 递归列出源码目录的内部 import，确认依赖单向、同层互不 import |
| 具体实现未泄漏 | 在源码目录 grep 具体类型名，逐条核对：只允许适配文件与便利构造的形参，其余命中即违规 |
| 错误格式统一 | grep `fmt.Errorf(` / `errors.New(`，排除带包名前缀的行；输出即违规 |

可运行的自检命令（目录、前缀白名单）由各项目 CLAUDE.md 落地；检测 grep 的写法与质量门槛见 [doc.md](doc.md) §五。
