# 数据迁移（migration）需求文档

> 本文定义 **migration 能力**：服务启动时按水位执行业务提供的 SQL 脚本目录（建表、初始化、结构修补），执行记录落在 `sys_migration` 表，已执行过的不重复执行。
>
> 语义基线是业务模板 [example/infra/mgx](../example/infra/mgx/)：水位、留痕、失败重试、失败即停等流程语义照搬；脚本目录形态照搬 [example/migration](../example/migration/)（embed + 版本目录 + `{版本号}_{序号}_{名称}.sql`）—— 业务侧用 slyorm 的能力替换 mgx 自研实现，脚本目录与存量 `sys_migration` 表**零改动**（见 §3 与 R3.7）。与模板的差异见 §3。
>
> 与 prd-slyorm §1.3"建表不在范围"的关系：**仍然不从模型推导 DDL**（脚本是业务手写 SQL）；本能力只负责**按序执行**业务脚本并留痕 —— 是"执行业务手写 SQL"的组织者，不是 DDL 生成器。
>
> 落地包：`migration/`（新，模块根）+ `dbwm/` 增补 `ExecScript`（R6，对 prd-dbwm §8"没有原始 SQL 逃生舱"与 R9.8"不做 DDL"做窄修订）+ 门面 `slyorm.DB.Migrate`（R1）。
>
> 评审记录见 [review.md](review.md)，其 M1 / M2 / F1–F12 / L1–L4 / D1–D6 意见已逐条权衡吸收（取舍见 §3、R2–R8、§6、§8）。

## 目录

1. [目标与范围](#1-目标与范围)
2. [术语](#2-术语)
3. [与 mgx 模板的对照（差异与迁移）](#3-与-mgx-模板的对照差异与迁移)
4. [需求条目 R1–R8](#4-需求条目)
5. [API 一览（业务集成）](#5-api-一览业务集成)
6. [与既有包 / 文档的关系（改造与同步清单）](#6-与既有包--文档的关系改造与同步清单)
7. [已知限制](#7-已知限制)
8. [已拍板](#8-已拍板)
9. [实现参考（文件结构）](#9-实现参考文件结构)

---

## 1. 目标与范围

### 1.1 目标

业务把初始化脚本放进自己的 `migration/` 目录（embed 进二进制），服务启动时调一次 `db.Migrate(ctx, fsys)`：

- 脚本按 `{版本号}_{序号}` 升序执行，已执行过（≤ 水位）的跳过；
- 每个脚本一个事务：成功的执行记录与脚本同事务提交；失败则回滚脚本、单独留痕（失败原因入库），下次启动重试；
- 脚本本身是业务手写 SQL（DDL / DML 皆可），本库不解析、不改写、不拆分。

一句话概括：**业务给"脚本目录（embed FS）"，migration 按 dbwm 的执行与事务语义跑脚本并留痕，业务拿到执行统计。**

### 1.2 在范围内

| # | 范围项 |
| --- | --- |
| S1 | 入口与集成：`migration.Run` + 门面 `db.Migrate`，脚本目录由业务 embed 提供 |
| S2 | 脚本装载与命名规则：解析期校验、排序、忽略非 `.sql` |
| S3 | 执行记录表 `sys_migration`：可移植 DDL、幂等建表、追加式留痕、存量表（mgx 形态）兼容 |
| S4 | 水位判定与跳过 |
| S5 | 脚本执行事务与失败留痕 |
| S6 | dbwm 增补 `ExecScript`：原样 SQL 执行的唯一入口 |
| S7 | 错误与执行统计（`Stats`）、留痕文本口径 |

### 1.3 不在范围内

| 不在范围 | 去向 |
| --- | --- |
| DDL 生成、模型到表结构的推导 | 不做（prd-slyorm §1.3 维持）；脚本是业务手写 SQL |
| 脚本内容解析（按 `;` 拆分、变量替换、条件执行） | 整文件原样交给驱动（见 §7 限制 1 / 2） |
| down / 回滚脚本、版本回退 | 只前进（水位只升不降） |
| 迁移执行的互斥（跨实例与进程内） | 调用方串行化（R8.3） |
| 脚本语法校验 | 数据库执行时报错，本层只透传 |
| 日志打点 | 返回 `Stats`，业务自己记日志（slyorm 无日志，prd-slyorm §1.3） |

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| **脚本** | 一个 `.sql` 文件：版本号、序号、名称（从文件名解析）与正文 |
| **水位** | 最近一次成功执行的 `(版本号, 序号)`；失败记录不抬水位 —— 失败的脚本下次启动要重试 |
| **执行记录** | `sys_migration` 表的一行：哪个脚本、何时、成败、失败原因 |
| **留痕** | 向 `sys_migration` 插一条执行记录（成功与失败都留） |
| **版本目录** | 脚本目录下 `v1.0/` 这类子目录：仅为人读的组织形式，不参与解析（版本号取自文件名） |
| **存量表** | mgx 时代已存在的 `sys_migration` 表（带自增 `id` 列、`error TEXT`），本能力不做结构升级（R3.7） |

---

## 3. 与 mgx 模板的对照（差异与迁移）

mgx 的流程语义（水位、跳过、一脚本一事务、失败留痕与重试、失败即停）**照搬**；下表只列有意差异。业务从 mgx 迁移 = 换调用入口（`New(db, fsys).Run` → `db.Migrate(ctx, fsys)`），**脚本目录、文件名与存量 `sys_migration` 表零改动**（兼容依据见 R3.7）。

| # | mgx（模板） | migration（本能力） | 说明 |
| --- | --- | --- | --- |
| ① | `New(db *sql.DB, fsys).Run(ctx)` | `migration.Run(ctx, *dbwm.DB, fsys)` / `db.Migrate` | 经 dbwm 执行：占位符、标识符引用、事务与错误语义随方言走 |
| ② | 记录表 DDL 带方言（反引号 + `AUTOINCREMENT` + `TEXT DEFAULT ''`） | 一份可移植 DDL（无引用符、无自增列、显式插值，见 R3.1） | 新库用新 DDL；**存量表不升级**（`IF NOT EXISTS` 空转，见 R3.7） |
| ③ | 直连 `*sql.DB` 自己 BeginTx | `db.Tx` + `dbwm.ExecScript`（R6） | ctx 传递、嵌套复用、事务结束报错等语义继承 prd-dbwm R9 |
| ④ | `id` 自增代理主键、`error` 列 | 新表无 `id` 列（追加式日志不需要代理主键）；**`error` 列名沿用** | `error` 在 SQLite / MySQL / PG 都不是保留字，改名收益只有可读性，却要付出整套存量升级路径 —— 不改；INSERT 不点名 `id`，存量表（有）与新表（无）同一份留痕代码 |
| ⑤ | 版本号 ≥ 0 | 版本号 ≥ 1、序号 ≥ 0 | 版本号 0 与初始水位 `(0,0)` 语义重叠（跳过判定 `(v,s) ≤ (0,0)` 下只有 `(0,0)` 永不执行，但版本号 0 没有区分度、易与"未执行"混淆）—— 解析期拦下（R2.1） |
| ⑥ | 不查 `(版本号, 序号)` 重复 | 解析期查重报错（R2.4） | 重复号在 mgx 里会双双执行且水位互相覆盖 |
| ⑦ | 空目录也建记录表（mgx 顺序是 ensureTable → watermark → load） | 空目录零副作用：不触库；装载校验先于建表（R2.4） | 没有脚本的服务不该多出一张表；写错文件名也不留半成品 |
| ⑧ | 失败留痕失败只打日志吞掉 | `errors.Join` 上抛（R5.3） | 留痕失败不吞、也不盖掉脚本错误 |
| ⑨ | lgx 打点（Info/Error） | 返回 `Stats`，不打点 | slyorm 无日志（prd-slyorm §1.3），业务拿统计自己记 |

---

## 4. 需求条目

每条需求以 **需求 / 规则 / 验收** 三段式给出。

### R1 入口与集成形态

**需求**：业务一个调用完成迁移；脚本目录业务自持（embed），本库不感知业务目录位置。

**规则**：

1. `migration.Run(ctx context.Context, db *dbwm.DB, fsys fs.FS) (Stats, error)` 是能力包的唯一入口 —— 无状态函数，不设 Service 结构（无配置项，见已拍板 1）；
2. 门面方法 `(*slyorm.DB).Migrate(ctx, fsys fs.FS) (migration.Stats, error)` 转发（门面持 `*dbwm.DB`，集成不需要暴露连接）；`Stats` 直接引用 `migration` 包类型，不在根包重复定义（类型归属口径见 prd-slyorm §3）；
3. `fsys` 是业务 embed 的脚本目录（根下按版本目录组织，见 R2）；`fsys` 为 nil → 报错；
4. **空目录零副作用**：装载后没有任何脚本 → 返回零值 `Stats`，不建表、不发任何 SQL；
5. `Stats{Scripts, Applied int}`：`Scripts` 是装载到的脚本总数，`Applied` 是本次实际执行数（跳过不计）；出错时返回**已执行数**的 `Stats` 与错误（业务能记下"跑到第几个"）。

**验收**：

- 对 `example/migration` 形态的 FS（embed.go + v1.0/*.sql）执行成功；
- nil FS → 报错；空目录 → `Stats{}` 且库中无 `sys_migration` 表；
- 出错时返回的 `Applied` 等于失败前已成功执行的脚本数。

### R2 脚本命名与装载

**需求**：脚本名自证次序；写错在执行任何脚本、触碰数据库之前暴露。

**规则**：

1. 文件名 `{版本号}_{序号}_{名称}.sql`（如 `1000_001_seed_admin.sql`）：
   - **版本号为正整数（≥1）**、序号为非负整数（≥0，`1000_000` 是既有脚本的常用起点）；
   - 版本号与序号**只允许十进制数字字符**（`+5`、`1e3`、带空格都不合法）；前导零按值解析（`007` 即 `7`），与 `7` 视为同号、由规则 4 的查重拦下；
   - **名称段可含下划线**：取第三个 `_` 之后的全部（`1000_001_seed_admin.sql` 的名称是 `seed_admin`），且非空；
   - 版本目录（`v1.0/`）只是组织形式，不参与解析；
2. 递归遍历 `fsys`（从根 `.` 起）；非 `.sql` 文件忽略（`readme.txt` 不阻断）；
3. 按 `(版本号, 序号)` 升序执行；
4. **解析期校验**（装载阶段全量解析，任一不合法 → 报错，一个脚本都不执行）：文件名格式不符、版本号 < 1、序号 < 0、`(版本号, 序号)` 与另一脚本重复 —— 错误信息带两个冲突文件的路径；
5. **装载与校验先于一切数据库操作**（建表、水位、执行）：装载失败零副作用 —— 不建表、不留痕、不发任何 SQL（mgx 的顺序是先建表再装载，本能力反过来）；
6. 读文件失败 → 报错（带文件路径）。

**验收**：

- `bad_name.sql`（段数不足）、`0_001_x.sql`（版本号 0）、`+5_001_x.sql`（符号）、两个 `1000_001_*.sql` → 报错，且库中**无 `sys_migration` 表**（零副作用）；
- `1000_007_a.sql` 与 `1000_7_b.sql` → 按重复编号报错（前导零同号）；
- `1000_001_seed_admin.sql` 的名称解析为 `seed_admin`（下划线不截断）；
- 目录里的 `readme.txt` 被忽略；
- `v1.0/1000_002_b.sql` 与 `v1.1/1100_001_c.sql` 按 1000→1100 的次序执行。

### R3 执行记录表（sys_migration）

**需求**：留痕表三库同构，建表幂等，mgx 存量表零改动可用。

**规则**：

1. 表名固定 `sys_migration`，DDL 是一份**可移植常量**：不带方言引用符，标识符全小写不加引号（均取三库非保留字：`version` / `sequence` / `name` / `file` / `success` / `error` / `executed_at`）。**DDL 经 `ExecScript` 原样执行（裸文本，不经 dbwm 渲染加引号）**；被 dbwm 按方言加引号渲染的只有留痕 INSERT 与水位查询 —— 两侧同名（一致性由规则 6 保证）：

   ```sql
   CREATE TABLE IF NOT EXISTS sys_migration (
     version     INTEGER       NOT NULL,
     sequence    INTEGER       NOT NULL,
     name        VARCHAR(200)  NOT NULL,
     file        VARCHAR(500)  NOT NULL,
     executed_at TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
     success     INTEGER       NOT NULL,
     error       VARCHAR(1000) NOT NULL
   )
   ```

2. **追加式日志**：无主键、无索引（每次 Run 的留痕数 = 本次执行数，量级是历史脚本数，见 §7 限制 7）；重试留新行，不 UPDATE 旧行；
3. **留痕与水位查询全部走 dbwm IR**：留痕是 `dbwm.Insert`（占位符、标识符引用随方言渲染）、水位是 `dbwm.Query` + `Qryone`（R4）—— migration 包不写带占位符的 SQL 文本、不自己碰连接；
4. 建表用 `ExecScript`（R6）在**装载校验之后**（R2.5）、有脚本时执行，`IF NOT EXISTS` 幂等；建表失败 → 报错终止；
5. **`name` 与 `file` 的取值口径**：`name` 取文件名第三段（名称段），`file` 取脚本在 FS 内的相对路径（如 `v1.0/1000_001_seed_admin.sql`）；
6. **列名单一来源**：`sys_migration` 的列名定义为 migration 包内常量，DDL 文本与留痕 IR 的 `Columns` 都由它派生 —— 两份手工副本漂移只会在运行时炸成 "no such column"，编译期就要挡住（实现形态见 §9）；
7. **存量表（mgx 形态）零改动兼容**，不做结构升级：
   - `CREATE TABLE IF NOT EXISTS` 对存量表是空转；
   - 留痕 INSERT **不点名 `id`**（也不点名 `executed_at`）：存量表的自增 `id` 由库自填，新表没有 `id` 列 —— 同一份留痕代码跑在两种结构上；
   - 列名沿用（`error` 等），存量库直接可用；
   - 列类型差异（存量 `error TEXT` / 新表 `VARCHAR(1000)`）不做 `ALTER`：入库文本统一按 R7.4 的应用侧截断兜底；
8. 不提供表名 / 列名的定制点（`sys_migration` 是约定名；业务要改名 = 自己维护一份，不在本能力）。

**验收**：

- 空库 Run 后表存在且列齐；二次 Run 不报错、表结构不变；
- **存量表兼容**：在 mgx 形态的表（`id` 自增主键 + `error TEXT`）上 Run：建表空转、留痕成功（`id` 由库自填、`error` 列写入）、水位读取正确；
- 留痕语句由 dbwm 渲染：ANSI 方言占位符是 `?`、PostgreSQL 是 `$1`（fakeConn 断言）；
- 重试同一脚本 → 表里该 `(version, sequence)` 有两行（一败一成）；
- `name` 存名称段、`file` 存相对路径（查表断言）。

### R4 水位与跳过

**需求**：只执行没执行过的；失败可重试。

**规则**：

1. 水位 = 最近一次成功记录的 `(版本号, 序号)`：`WHERE success = 1` 按 `version DESC, sequence DESC` 取第一行（`dbwm.Query{Sorts: ["-version", "-sequence"]}` + `Qryone` 自带 LIMIT 1）；无成功记录 → `(0, 0)`（`ErrNoRows` 转零值，不是错误）；
2. 跳过判定：脚本 `(v, s)` ≤ 水位 → 跳过（先比版本号、再比序号）；**失败记录不抬水位** —— 失败的脚本下次 Run 重试；
3. **失败即停（R5.4）是失败可重试的前提**，两条规则绑定生效：失败后若继续执行后续脚本，后续的成功会把水位抬过失败脚本，使其此后永远 ≤ 水位、永不再跑 —— 所以本能力不做"留痕但继续"的选项；
4. 水位每次 Run 开始时读一次，循环内不刷新 —— 脚本已按 `(v, s)` 升序排好，升序遍历 + 与初始水位比较即正确；
5. **水位只前进**：往水位之下补历史脚本、把已执行脚本改小编号，都不会执行（见 §7 限制 3）。

**验收**：

- 首次 Run 全部执行；紧接二次 Run 全部跳过、留痕不增；
- 失败后水位不变（按 success=1 查不到新记录），修复脚本后重跑成功；
- 已执行到 `(1000, 5)` 时，新放入的 `(1000, 3)` 被跳过、`(1000, 6)` 被执行。

### R5 脚本执行与留痕

**需求**：一个脚本一个事务；成败都留痕；失败即停。

**规则**：

1. 每个脚本：`db.Tx(ctx, fn)`，`fn` 内 `ExecScript(正文)` + 留痕 `success=1` —— **成功记录与脚本同事务**提交，同生共死；
2. 脚本失败 → 事务回滚（脚本与成功记录都不落库），随后在**该脚本事务之外**留痕 `success=0`、`error=错误文本`（截断口径见 R7.4）—— 外层无事务时直连落库；失败留痕必须落库：脚本已回滚，库上无痕迹，这条记录是唯一线索（ctx 已取消的例外见 R8.2）；
3. 失败留痕自身失败 → `errors.Join(脚本错误, 留痕错误)` 上抛：不吞留痕失败，也不让它盖掉脚本错误；
4. 任一脚本失败 → **停止**（这是 R4.2"失败可重试"的前提，见 R4.3），其后脚本留待下次 Run；已提交的脚本保持；
5. **在外层 `db.Tx` 内调用 Run** → 复用外层事务（prd-dbwm R9.4 嵌套语义：dbwm.Tx 对嵌套调用不回滚、不开保存点）：
   - **失败脚本已执行的部分语句留在外层事务里**，本层无法撤销 —— 由外层回滚兜底；
   - 因此 **Run 返回的错误必须冒泡到外层**：外层吞掉错误再提交 = 提交了半截脚本；
   - 全部脚本与全部留痕共用外层事务，失败留痕也随外层回滚消失 —— 原子性归外层；不禁止、不特判；
6. **空白脚本是空操作成功**：`ExecScript` 对空白文本不发语句、返回 nil（R6.3）→ 留 `success=1`、照常抬水位 —— 不是错误，别当 bug 排查（要"占个号又不做事"正是空白脚本的用途）。

**验收**：

- 失败脚本：正文效果不落库（回滚）、留 `success=0` 且 `error` 非空、其后的脚本未执行；
- 修复后重跑：留 `success=1`，脚本报错原文（截断后）可从 `error` 列看到；
- 成功路径：脚本与 `success=1` 记录同事务（留痕失败时脚本也回滚）；
- 外层 `db.Tx` 内 Run 且某脚本失败 → 外层回滚 → **脚本正文效果与 `sys_migration` 新行都不落库**；
- 空白脚本 → 留 `success=1`、水位抬过它、无 SQL 发出。

### R6 dbwm 增补：ExecScript

**需求**：迁移要原样执行业务 SQL，dbwm 目前只有 IR 入口 —— 增补一个窄口子。

**规则**：

1. `(*dbwm.DB).ExecScript(ctx context.Context, script string) error`：把 `script` 原样交给驱动执行 —— 不渲染、不拆分、无绑定参数、返回值只有 error（多语句时 RowsAffected 不可靠，不暴露）；
2. 走 `execerOf(ctx)`：ctx 带事务即在事务内执行（R5 依赖此语义）；
3. 空白文本 → 直接返回 nil，不发语句（MySQL 对空查询报 1065，别把空脚本当失败）；
4. 错误：`dbwm: 执行脚本失败: %w` —— **不含脚本正文**（可能极长；脚本定位信息由 migration 层的文件名补上）。这是 prd-dbwm R6.1"错误信息带语句文本"约定的**例外**，prd-dbwm 增补 R11 时写明（见 §6）；
5. **这是对 prd-dbwm"没有原始 SQL 逃生舱"（§8.10）与"手写 SQL 由调用方自己执行、不做 DDL"（R9.8）的窄修订**（prd-dbwm 增补 R11 同步）："逃生舱"指业务查询绕过 IR 拼条件（仍然禁止，维持原决策）；初始化脚本是声明式迁移能力的一部分，经此**唯一**入口执行 —— 无参数、命名即语义，不是通用拼 SQL 的口子。取回底层 `*sql.DB` 的逃生舱**仍然没有**（prd-dbwm §8.6 维持）。

**验收**：

- ctx 带事务时语句在事务内执行（fakeConn 断言走的是 tx 执行器）；
- 空白文本不发语句、返回 nil；
- 驱动错误可经 `errors.Is` / `errors.As` 穿透。

### R7 错误、统计与留痕文本

**需求**：写错了能定位到脚本；执行结果可观测；入库的错误文本口径明确。

**规则**：

1. 错误前缀 `migration: `（AGENTS §7 前缀白名单同步加）；包装底层错误一律 `%w`；
2. 执行类错误带脚本文件路径：`migration: 执行脚本 v1.0/1000_001_x.sql 失败: %w`；装载类错误带文件路径与期望格式：`migration: 脚本文件名须为 {版本号}_{序号}_{名称}.sql: %s`；
3. `Stats` 语义见 R1.5：成功时 `Applied = Scripts - 跳过数`；出错时 `Applied` = 失败前已成功数（失败的那个不计）；
4. **入库的 `error` 文本 = 驱动错误按 1000 字符（rune，非字节）截断**：UTF-8 按字节切会截坏多字节字符，且 PG / MySQL 的 `VARCHAR(1000)` 本就按字符计；**上抛的错误不截断**（调用方看到原文）。存量表的 `error` 是 `TEXT` 无长度限制，截断口径统一由应用侧保证（R3.7）。

**验收**：

- 错误信息里能看到脚本路径；`errors.Is(err, 驱动错误)` 成立；
- 部分成功后出错 → `Stats{Scripts: 3, Applied: 1}` 形态的返回；
- 超长错误文本：入库行被截到 ≤1000 字符且多字节字符完好，上抛错误为原文。

### R8 幂等与并发边界

**需求**：重复启动安全；中断可恢复；并发边界明确。

**规则**：

1. Run 幂等：无新脚本时一次 Run 只读水位、零执行、零留痕（不新建表 —— 表已存在，`IF NOT EXISTS` 空转）；
2. ctx 取消 / 超时 → 当前脚本事务回滚、错误返回；已提交的脚本保持，下次 Run 从断点续跑。**失败留痕用同一个 ctx，此时同样写不进去** —— 库上无痕属预期（ctx 已失效），不是 bug：脚本下次会重试；`errors.Join` 会把脚本错误与留痕错误一起抛出，调用方判定依据是错误链里有 `context.Canceled` / `context.DeadlineExceeded`；
3. **不做迁移执行的互斥，进程内与跨实例同一口径**：同一进程两个 goroutine 同时 Run、或两个句柄指向同一个库 → 各自读水位、各自执行，同一脚本可能执行两遍（留痕两行 `success=1`）。启动期单次调用是常态；要并发首启，调用方自行串行化（加锁需要库型相关原语，跨实例也锁不住，不值当）。

**验收**：

- ctx 已取消时 Run → 报错，本轮已开启的事务回滚、无 `success=1` 记录；
- 同一 FS 连续 Run 两次，第二次零留痕。

---

## 5. API 一览（业务集成）

```go
import (
    "github.com/icdb37/slyorm"
    appmg "myapp/migration" // 业务自己的脚本目录包（与 github.com/icdb37/slyorm/migration 无关）
)

// 业务侧脚本目录（形态同 example/migration）：
// myapp/migration/embed.go
//   //go:embed v1.0/*.sql
//   var FS embed.FS
// myapp/migration/v1.0/1000_000_schema.sql …

func main() {
    db, err := slyorm.Open("sqlite", "app.db") // 驱动由调用方注册
    if err != nil { … }

    // 启动时一次：按水位执行未跑过的脚本，业务拿统计自己打日志
    st, err := db.Migrate(ctx, appmg.FS)
    // st.Scripts / st.Applied

    … // 之后正常使用 Collection
}

// 不经门面（自己持 *dbwm.DB 的场景）：
st, err := migration.Run(ctx, wm, fsys)
```

- 业务从 mgx 迁移：`mgx.New(db, fsys).Run(ctx)` → `db.Migrate(ctx, fsys)`，脚本目录、文件名与存量 `sys_migration` 表不动（§3 / R3.7）；
- MySQL 下 DSN 需带 `multiStatements=true`（多语句整发，见 §7 限制 1）。

---

## 6. 与既有包 / 文档的关系（改造与同步清单）

| 位置 | 改动 |
| --- | --- |
| `dbwm/`（exec.go 或新 exec_script.go） | 增补 `ExecScript`（R6） |
| `migration/`（新包，模块根） | 本能力；只 import `dbwm` 与标准库（不 import 根包、不碰兄弟包、不 import database/sql） |
| 根包 `migrate.go`（新文件） | `(*DB).Migrate` 转发 `migration.Run`；`slyorm.go` 的文件分工注释补一行 |

文档同步（实现落地时一并改，不留"同步"字样）：

- [prd-dbwm.md](prd-dbwm.md)：
  - **R9.8**（"建表等手写 SQL 由调用方在自己的程序里执行…不做 DDL"）窄修订 —— `ExecScript` 是唯一例外口子，语义见本文 R6；
  - 需求条目增补 **R11 ExecScript**：含两条对既有约定的例外声明 —— 错误**不带**脚本正文（对 R6.1"错误信息带语句文本"的例外）、可执行 DDL（对 R9.8 的窄修订）；
  - §8 限制 10（无原始 SQL 逃生舱）补窄修订说明；§9 已拍板加一行；API 一览与文件结构补 `ExecScript`；
- [prd-slyorm.md](prd-slyorm.md)：§8 限制 10 —— 前半句补"初始化脚本的**执行**见 prd-migration（仍然不从模型推导 DDL）"，**后半句"用 `slyorm.Open` 打开时…要自己执行建表 SQL 就不要用它打开"改写**（`Open` + `db.Migrate` 已覆盖该场景，原句与新能力互相打脸）；§6 API 一览补 `db.Migrate`；
- **AGENTS.md（与 `.claude/CLAUDE.md` 是硬链接，改写类操作会断链，改后需重建，见 `.claude/rules/doc.md` §一）**：
  - §2 包表加 `migration/` 一行；
  - §7 自检 grep 的路径列表与错误前缀白名单加 `migration`；
  - §8"不做建表 / DDL / 表结构迁移"改口径："**不从模型生成 DDL、不做表结构推导**；初始化脚本的按序执行见 prd-migration"；
  - "当前进度"随实现更新；
- [prd.md](prd.md)：分层图 `business → slyorm → dbwm → sql.DB` 补 migration 的位置（`business → migration → dbwm`，与 slyorm 并列的门面入口）。

---

## 7. 已知限制

1. **多语句整发依赖驱动**：SQLite（modernc.org/sqlite）、PostgreSQL（lib/pq、pgx）支持多语句 Exec；MySQL 需在 DSN 加 `multiStatements=true`（调用方责任，本库不猜不改 DSN）；
2. **不拆分语句**：含触发器 / 存储过程体的脚本（内嵌 `;`）不受影响（整发）；代价是脚本失败只能定位到文件、定位不到语句；
3. **水位只前进**：往水位之下补历史脚本、改小已执行脚本的编号，都不会执行；要重跑一个脚本只能以更大的编号新增；
4. **MySQL 的 DDL 隐式提交**：脚本内 DDL 失败时，本脚本里已执行的 DDL 无法回滚（记录仍留 `success=0`）—— 重试要求脚本幂等（`IF NOT EXISTS` / `OR IGNORE`，与 example 既有脚本风格一致）；同理，外层事务里的 DDL 失败也无法靠回滚撤销；
5. `executed_at` 用数据库时间（`CURRENT_TIMESTAMP`），不是应用时间；
6. **迁移执行无互斥**（R8.3）：跨实例与进程内一致，调用方 / 运维保证串行；
7. `sys_migration` 只增不清；水位查询是无索引的全表扫 + 排序 —— 量级是历史脚本执行数，启动期一次性操作，可忽略；
8. **PG / MySQL 无真库验证**：当前验证以内存 SQLite + fakeConn 为主。列名已按三库非保留字核对（`error` / `version` / `sequence` / `name` / `file` / `success` / `executed_at`），**上线前把"三库执行 `sys_migration` 建表 DDL + 跑一次 Run"列为人工验证项**。

---

## 8. 已拍板

| # | 议题 | 结论 | 章节 |
| --- | --- | --- | --- |
| 1 | 能力归属 | 新能力包 `migration/`（模块根）+ 门面 `Migrate` 转发；不进 dbwm（它是流程能力，不是 SQL 渲染能力）；无状态函数入口，不设 Service / 配置 | R1 §9 |
| 2 | 执行入口 | dbwm 增补 **`ExecScript`**（无参数、ctx 感知事务、空白即 no-op、错误不带正文）；**不做**通用 raw `Exec`、不暴露连接给能力包 | R6 |
| 3 | 记录表 | 固定 `sys_migration`、一份可移植 DDL、追加式、新表无自增 `id` 列；**`error` 列名沿用 mgx**（三库均非保留字，改名收益只有可读性、代价是存量升级路径）；列名常量单一来源；无定制点 | R3 |
| 4 | 存量表兼容 | mgx 存量表**零改动**：`IF NOT EXISTS` 空转、INSERT 不点名 `id`、列名沿用；列类型差异（TEXT / VARCHAR）由应用侧截断兜底 —— **不做** `ALTER` 升级、不做补列脚本 | R3.7 §3 |
| 5 | 水位方案 | 照搬 mgx：最近一次成功 `(v, s)`，失败不抬水位，≤ 水位跳过；**不做**按文件名集合比对 | R4 |
| 6 | 版本号规则 | 版本号 ≥ 1（版本号 0 与初始水位 `(0,0)` 语义重叠、无区分度）、序号 ≥ 0（`1000_000` 是既有起点）；数字段只认十进制数字字符、前导零按值解析由查重拦、名称段可含下划线 | R2 |
| 7 | 解析期查重 | `(版本号, 序号)` 重复报错（mgx 不查）；装载校验先于建表，装载失败零副作用 | R2 |
| 8 | 事务粒度 | 一脚本一事务；成功留痕同事务、失败留痕独立提交；失败即停 —— 且失败即停是失败可重试的前提（绑定不变式） | R4.3 R5 |
| 9 | 外层事务 | Run 可在 `db.Tx` 内：复用外层、**失败脚本的部分效果留在外层事务**、Run 的错误必须冒泡（外层吞错提交 = 提交半截脚本）；不禁止不特判 | R5.5 |
| 10 | 日志 | 不打点，返回 `Stats` 由业务记（slyorm 无日志） | R1 R7 |
| 11 | 空目录 | 零副作用：不建表不发 SQL | R1.4 |
| 12 | 脚本不改写 | 原样整发：不拆分、不替换变量、不做条件执行；空白脚本 = 空操作成功、照常抬水位 | R5.6 R6 |
| 13 | 留痕文本 | 入库 `error` 按 1000 **字符（rune）**截断、上抛原文不截断 | R7.4 |
| 14 | 并发 | 不加锁（进程内与跨实例同一口径），调用方串行化；ctx 取消时失败留痕写不进属预期 | R8 |

---

## 9. 实现参考（文件结构）

> 本节是实现指引，不是需求约束。目标布局沿用既有包的分工原则：**一个包一个能力，按关注点分文件**。

```
migration/                    数据迁移（新，package migration）
  migration.go                包注释与对外 API：Run —— 装载 → 建表 → 水位 → 逐脚本执行留痕
  script.go                   脚本装载：文件名解析 {版本号}_{序号}_{名称}.sql（SplitN 取第三段之后全部）、
                              递归遍历、排序与解析期校验（格式 / 字符集 / 取值 / 查重）
  record.go                   执行记录：列名常量与建表 DDL（由常量拼出）、留痕（Insert IR）、
                              水位查询（Query IR + Qryone，RowFactory 闭包扫两个 int）

dbwm/exec.go                  增补 ExecScript（或独立 exec_script.go，随实现取舍）
migrate.go                    门面：(*DB).Migrate 转发 migration.Run（根包新文件）
```

**落地时要注意的五件事**：

1. **migration 包只 import `dbwm`**：留痕、水位、建表全走 `dbwm.DB` 的既有入口 + `ExecScript`；不 import `database/sql`、不持连接 —— AGENTS §7 自检天然干净；
2. **列名常量单一来源**（R3.6）：`record.go` 里列名只定义一遍，DDL 文本由常量拼出、留痕 IR 的 `Columns` 引用同一常量；并加一个测试断言 DDL 文本中的列名集合与留痕 IR 的列名集合一致；
3. **测试**（内存 SQLite `Open("sqlite", ":memory:")` + `fstest.MapFS`，mgx_test.go 同款手法；用例名对齐 R 编号）：`TestLoadScripts`（R2：格式 / 查重 / 零副作用 / 下划线名称段）、`TestWatermarkSkip`（R4：低号跳、高号跑、失败不抬水位）、`TestScriptTx`（R5：成功同事务、失败回滚留痕、空白脚本）、`TestExecScript`（R6，dbwm 侧：事务内执行、空白 no-op、错误穿透）、`TestRunStats`（R1 / R7：统计与截断）、`TestEmptyDir`（R1.4）、`TestLegacyTable`（R3.7：mgx 形态存量表零改动）、`TestNestedTx`（R5.5：外层回滚连脚本正文一起消）、`TestCtxCancel`（R8.2：取消时留痕写不进属预期）；
4. **水位闭包**：`Qryone` 的 RowFactory 用闭包扫 `(version, sequence)`，`ErrNoRows` 转零值水位；
5. **文档同步**按 §6 清单一并落地（prd-dbwm R9.8 / R11、prd-slyorm §8.10、AGENTS.md 三处 + 硬链接重建、prd.md 分层图）。
