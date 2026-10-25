package dbwm

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// R2 方言接口
// ---------------------------------------------------------------------------

// 加引号与转义由 dbwm 统一做（QuoteIdent），方言只给引用字符（QuoteChar）。
func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		want string
	}{
		{name: "ANSI 用双引号", d: ANSI, want: `"name"`},
		{name: "MySQL 用反引号", d: MySQL, want: "`name`"},
		{name: "PostgreSQL 用双引号", d: PostgreSQL, want: `"name"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QuoteIdent(c.d, "name"); got != c.want {
				t.Fatalf("QuoteIdent = %s，期望 %s", got, c.want)
			}
		})
	}
	// 引号字符本身 doubling 转义：注入尝试只会变成"不存在的标识符"。
	if got := QuoteIdent(ANSI, `a"b`); got != `"a""b"` {
		t.Fatalf(`含引号的标识符 = %s，期望 "a""b"`, got)
	}
	if got := QuoteIdent(MySQL, "a`b"); got != "`a``b`" {
		t.Fatalf("含反引号的标识符 = %s，期望 `a``b`", got)
	}
}

func TestPlaceholder(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		want []string
	}{
		{name: "ANSI 是问号", d: ANSI, want: []string{"?", "?", "?"}},
		{name: "MySQL 是问号", d: MySQL, want: []string{"?", "?", "?"}},
		{name: "PostgreSQL 连续编号", d: PostgreSQL, want: []string{"$1", "$2", "$3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i, want := range c.want {
				if got := c.d.Placeholder(i + 1); got != want {
					t.Fatalf("第 %d 个占位符 = %s，期望 %s", i+1, got, want)
				}
			}
		})
	}
}

func TestLimit(t *testing.T) {
	cases := []struct {
		name          string
		d             Dialect
		limit, offset int
		want          string
	}{
		{name: "ANSI", d: ANSI, limit: 10, offset: 20, want: "LIMIT 10 OFFSET 20"},
		{name: "MySQL", d: MySQL, limit: 10, offset: 20, want: "LIMIT 10 OFFSET 20"},
		{name: "PostgreSQL", d: PostgreSQL, limit: 10, offset: 20, want: "LIMIT 10 OFFSET 20"},
		{name: "偏移为 0", d: ANSI, limit: 10, offset: 0, want: "LIMIT 10"},
		{name: "不支持分页", d: noPagingDialect{}, limit: 10, offset: 20, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Limit(c.limit, c.offset); got != c.want {
				t.Fatalf("Limit = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestMaxParams(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		want int
	}{
		{name: "ANSI 是 SQLite 的上限", d: ANSI, want: 32766},
		{name: "MySQL", d: MySQL, want: 65535},
		{name: "PostgreSQL", d: PostgreSQL, want: 65535},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.MaxParams(); got != c.want {
				t.Fatalf("MaxParams = %d，期望 %d", got, c.want)
			}
		})
	}
}

func TestBatchAutoIDs(t *testing.T) {
	t.Run("SQLite 连续", func(t *testing.T) {
		ids, ok := ANSI.BatchAutoIDs(10, 3)
		if !ok {
			t.Fatalf("期望可推算")
		}
		want := []int64{8, 9, 10}
		for i := range ids {
			if ids[i] != want[i] {
				t.Fatalf("自增值 = %v，期望 %v", ids, want)
			}
		}
	})
	t.Run("MySQL 多行不可靠", func(t *testing.T) {
		if _, ok := MySQL.BatchAutoIDs(10, 3); ok {
			t.Fatalf("MySQL 多行插入不应推算自增值")
		}
	})
	t.Run("MySQL 单行可靠", func(t *testing.T) {
		ids, ok := MySQL.BatchAutoIDs(10, 1)
		if !ok || len(ids) != 1 || ids[0] != 10 {
			t.Fatalf("单行自增值 = %v（可靠 %v），期望 [10]", ids, ok)
		}
	})
	t.Run("PostgreSQL 不可靠", func(t *testing.T) {
		if _, ok := PostgreSQL.BatchAutoIDs(10, 1); ok {
			t.Fatalf("PostgreSQL 应走 RETURNING，不推算自增值")
		}
	})
}

func TestReturning(t *testing.T) {
	if got := PostgreSQL.Returning("id"); got != ` RETURNING "id"` {
		t.Fatalf("Returning = %q，期望 %q", got, ` RETURNING "id"`)
	}
	if got := ANSI.Returning("id"); got != "" {
		t.Fatalf("ANSI 不该支持 RETURNING，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// R9 库型识别
// ---------------------------------------------------------------------------

func TestDialectForDriver(t *testing.T) {
	cases := []struct {
		driver string
		want   Dialect
	}{
		{driver: "sqlite", want: ANSI},
		{driver: "sqlite3", want: ANSI},
		{driver: "mysql", want: MySQL},
		{driver: "mariadb", want: MySQL},
		{driver: "postgres", want: PostgreSQL},
		{driver: "pgx", want: PostgreSQL},
	}
	for _, c := range cases {
		t.Run(c.driver, func(t *testing.T) {
			got, ok := DialectForDriver(c.driver)
			if !ok {
				t.Fatalf("驱动 %q 没有映射到方言", c.driver)
			}
			if got.Name() != c.want.Name() {
				t.Fatalf("驱动 %q 的方言 = %s，期望 %s", c.driver, got.Name(), c.want.Name())
			}
		})
	}
	if _, ok := DialectForDriver("nosuchdriver"); ok {
		t.Fatalf("未知驱动不该映射出方言")
	}
}

// noPagingDialect 是不支持分页的方言，用于验证渲染期报错。
type noPagingDialect struct{ ansi }

func (noPagingDialect) Name() string { return "nopaging" }

func (noPagingDialect) Limit(int, int) string { return "" }

// ---------------------------------------------------------------------------
// R9 库型探测
// ---------------------------------------------------------------------------

func TestDetectSQLite(t *testing.T) {
	db := newTestDB(t)
	d, err := Detect(context.Background(), db.conn)
	if err != nil {
		t.Fatalf("探测库型失败: %v", err)
	}
	if d.Name() != ANSI.Name() {
		t.Fatalf("探测到的方言 = %s，期望 %s", d.Name(), ANSI.Name())
	}
}
