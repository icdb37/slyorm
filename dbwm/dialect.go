package dbwm

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 方言
// ---------------------------------------------------------------------------

// Dialect 是数据库方言：标准 SQL 覆盖不到、必须按数据库调整的少数几点。
//
// 核心语句是标准 SQL —— 条件组的括号、AND/OR/NOT 的连接、算子关键字
// （= <> LIKE IN IS NULL…）、ORDER BY 的写法、COUNT(*)、INSERT INTO … VALUES
// 的骨架都由 dbwm 的通用拼装器负责，不进方言。
//
// 方言只负责五件事：
//   - 标识符的引用字符（ANSI 用 "，MySQL 用 `）；加引号与转义由 dbwm
//     统一做（见 QuoteIdent），方言不拼字符串；
//   - 占位符风格（? 与 PostgreSQL 的 $1、$2…）；
//   - 分页写法（Limit）；
//   - 写侧的取回生成值（Returning / BatchAutoIDs）；
//   - 一条语句里绑定参数的上限（MaxParams）。
//
// 方言实现是并发安全的只读对象（嵌入的空结构体），可以被多个句柄共享。
type Dialect interface {
	// Name 方言名，用于错误信息。
	Name() string
	// QuoteChar 返回标识符的引用字符：ANSI / PostgreSQL 是 "，MySQL 是 `。
	QuoteChar() string
	// Placeholder 返回第 n 个占位符。
	Placeholder(n int) string
	// Limit 返回分页子句（如 "LIMIT 10 OFFSET 20"）；
	// 返回空串表示该库不支持分页。
	Limit(limit, offset int) string
	// Returning 返回"插入后取回生成值"的 SQL 子句（含前导空格）；
	// 返回空串表示该方言不支持，只能靠 sql.Result.LastInsertId() 取。
	Returning(column string) string
	// BatchAutoIDs 从 LastInsertId 推出一批 n 个自增值（按行序）；
	// 第二个返回值 false 表示该方言下推算不可靠 —— 调用方应当报错，
	// 而不是回填可能错位的值。
	BatchAutoIDs(lastInsertID int64, n int) ([]int64, bool)
	// MaxParams 是一条语句里绑定参数的上限，批量插入据此分块。
	MaxParams() int
}

// QuoteIdent 给标识符加引号：方言给引用字符（QuoteChar），加引号与转义
// （引号字符 doubling）统一在这里做 —— 渲染器与自定义算子共用这一份逻辑。
func QuoteIdent(d Dialect, ident string) string {
	q := d.QuoteChar()
	return q + strings.ReplaceAll(ident, q, q+q) + q
}

// ANSI 是标准 SQL 方言：双引号标识符 + ? 占位符。
//
// SQLite 直接用它；MySQL 在 sql_mode 含 ANSI_QUOTES 时也能用
// （否则请改用 MySQL 方言）。
var ANSI Dialect = ansi{}

// MySQL 是 MySQL 方言：反引号标识符 + ? 占位符。
var MySQL Dialect = mysql{}

// PostgreSQL 是 PostgreSQL 方言：双引号标识符 + $1 占位符 + RETURNING 取自增值。
var PostgreSQL Dialect = postgres{}

// OrDefaultDialect 方言为空时用标准 SQL 方言。
func OrDefaultDialect(d Dialect) Dialect {
	if d == nil {
		return ANSI
	}
	return d
}

// ansi 是标准 SQL 方言。
type ansi struct{}

func (ansi) Name() string { return "ansi" }

func (ansi) QuoteChar() string { return `"` }
func (ansi) Placeholder(n int) string {
	return "?"
}

// Limit 返回 "LIMIT n"（偏移为 0）或 "LIMIT n OFFSET m"。
func (ansi) Limit(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	return fmt.Sprintf("LIMIT %d", limit)
}

// Returning 恒为空：标准 SQL 没有"插入后取回生成值"的通用写法，
// 有 LastInsertId 的数据库走 sql.Result.LastInsertId()。
func (ansi) Returning(string) string { return "" }

// BatchAutoIDs 推算一批自增值：SQLite（本方言的主要后端）的 LastInsertId
// 是最后一行的值，同一语句内自增值连续，因此 lastID-n+1 … lastID 可靠。
func (ansi) BatchAutoIDs(lastInsertID int64, n int) ([]int64, bool) {
	first := lastInsertID - int64(n) + 1
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = first + int64(i)
	}
	return ids, true
}

// MaxParams：SQLite 3.32 起是 32766。
func (ansi) MaxParams() int { return 32766 }

// mysql 是 MySQL 方言：标识符用反引号，其余同标准 SQL。
type mysql struct{ ansi }

func (mysql) Name() string { return "mysql" }

func (mysql) QuoteChar() string { return "`" }

// BatchAutoIDs：MySQL 多行 INSERT 的 LastInsertId 是**第一行**的自增值
// （与 SQLite 的"最后一行"相反）。单行时第一行就是唯一行，可靠；
// 多行时 MySQL 8 起 innodb_autoinc_lock_mode 默认为 2（interleaved），
// 并发下一条语句分到的自增值不保证连续 —— 不推算，交由调用方报错引导。
func (mysql) BatchAutoIDs(firstInsertID int64, n int) ([]int64, bool) {
	if n == 1 {
		return []int64{firstInsertID}, true
	}
	return nil, false
}

// MaxParams：MySQL 的占位符上限 65535。
func (mysql) MaxParams() int { return 65535 }

// postgres 是 PostgreSQL 方言：占位符是 $1、$2…，自增值用 RETURNING 取回。
type postgres struct{ ansi }

func (postgres) Name() string { return "postgres" }
func (postgres) Placeholder(n int) string {
	return fmt.Sprintf("$%d", n)
}

// Returning 生成 ` RETURNING "列名"`。
func (p postgres) Returning(column string) string {
	if column == "" {
		return ""
	}
	return " RETURNING " + QuoteIdent(p, column)
}

// BatchAutoIDs 恒不可靠：PostgreSQL 没有 LastInsertId，
// 自增值一律走 RETURNING 按行取回（见 Returning）。
func (postgres) BatchAutoIDs(int64, int) ([]int64, bool) {
	return nil, false
}

// MaxParams：PostgreSQL 的占位符上限 65535。
func (postgres) MaxParams() int { return 65535 }
