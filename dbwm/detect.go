package dbwm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 库型识别
// ---------------------------------------------------------------------------

// driverDialects 常见驱动注册名 → 方言。
//
// 键是 sql.Register 的名字 —— modernc.org/sqlite 与 mattn/go-sqlite3、
// go-sql-driver/mysql、lib/pq（"postgres"）与 pgx 的标准库模式（"pgx"）。
//
// 拿驱动名先查这里：查得到就直接定方言（不连库探测，省一次连接），
// 查不到再探测（见 Detect）。
var driverDialects = map[string]Dialect{
	"sqlite":   ANSI,
	"sqlite3":  ANSI,
	"mysql":    MySQL,
	"mariadb":  MySQL,
	"postgres": PostgreSQL,
	"pgx":      PostgreSQL,
}

// DialectForDriver 按驱动注册名查方言。
//
// 第二个返回值 false 表示这个驱动名不在表里 —— 调用方再走探测（见 Detect）。
func DialectForDriver(driver string) (Dialect, bool) {
	d, ok := driverDialects[driver]
	return d, ok
}

// RowQuerier 是探测库型用到的最小依赖：能查一行。
//
// *sql.DB 与 *sql.Tx 都满足它。
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Detect 识别连接的数据库类型，返回对应的方言。
//
// 探测靠两条语句（标准 SQL 之外的少数例外）：
//
//	SELECT version()        PostgreSQL / MySQL 都支持，返回串里带库名
//	SELECT sqlite_version() SQLite 专用
//
// 探测会真的连一次库，因此 conn 必须可用；识别不出来时返回错误，
// 由调用方显式指定方言。多数情况下不用走到这里 —— Open 拿驱动名
// 就能映射出方言（见 DialectForDriver）。
func Detect(ctx context.Context, conn RowQuerier) (Dialect, error) {
	var v string
	errVersion := conn.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	if errVersion == nil {
		low := strings.ToLower(v)
		switch {
		case strings.HasPrefix(low, "postgresql"):
			return PostgreSQL, nil
		case strings.Contains(low, "mariadb"):
			return MySQL, nil
		case low != "" && low[0] >= '0' && low[0] <= '9':
			// MySQL 家族的 version() 就是版本号（"8.0.32"、"8.0.32-log"），
			// 衍生版（Percona、TiDB…）同样数字开头，反引号 + ? 对它们都适用。
			return MySQL, nil
		}
		// 有 version() 但认不出是谁（如 CockroachDB 这类 PG 兼容库）：
		// 不猜 —— 猜错方言会让 RETURNING / 自增回填静默失效。
		return nil, fmt.Errorf("dbwm: 无法识别数据库类型：version() = %q，请显式指定方言（WithDialect）", v)
	}
	var sv string
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sv); err == nil {
		return ANSI, nil
	}
	// 走到这里说明两条探测语句都失败：version() 的错误原样带回去 ——
	// 它往往不是"库型不认识"，而是连接本身没通（网络、认证）。
	return nil, fmt.Errorf("dbwm: 无法识别数据库类型（version() 与 sqlite_version() 都不可用；若是连接问题请先修复）: %w", errVersion)
}
