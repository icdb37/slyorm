package rows

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/icdb37/slyorm/dbwm"
	"github.com/icdb37/slyorm/query"

	_ "modernc.org/sqlite"
)

var tctx = context.Background()

// 开库：内存 SQLite，自持 *sql.DB 建表、NewSQL 注入 dbwm 句柄。
func openDB(t *testing.T) (*dbwm.DB, *sql.DB) {
	t.Helper()
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	base.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = base.Close() })
	return dbwm.NewSQL(base), base
}

// 全行模型与投影模型。
type row struct {
	ID       int64      `dbx:"id,autopk"`
	Name     string     `dbx:"name"`
	Age      int        `dbx:"age"`
	Nick     *string    `dbx:"nick"`
	Born     time.Time  `dbx:"born"`
	Died     *time.Time `dbx:"died"`
	Payload  []string   `dbx:"payload,json"`
	AddrCity string     `dbx:"addr_city"`
	AddrSt   string     `dbx:"addr_st"`
}
type addr struct {
	AddrCity string `dbx:"addr_city"`
	AddrSt   string `dbx:"addr_st"`
}

const ddl = `CREATE TABLE t (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	age INT NOT NULL DEFAULT 0,
	nick TEXT,
	born TIMESTAMP NOT NULL DEFAULT '0001-01-01 00:00:00',
	died TIMESTAMP,
	payload TEXT NOT NULL DEFAULT '',
	addr_city TEXT NOT NULL DEFAULT '',
	addr_st TEXT NOT NULL DEFAULT ''
)`

func seed(t *testing.T, base *sql.DB) {
	t.Helper()
	if _, err := base.ExecContext(tctx, ddl); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	const ins = `INSERT INTO t(name, age, nick, born, died, payload, addr_city, addr_st) VALUES(?,?,?,?,?,?,?,?)`
	// 行 1：指针列非 NULL、时间字符串、json 数组。
	if _, err := base.ExecContext(tctx, ins,
		"张三", 30, "三少", "2026-01-02 03:04:05", nil,
		`["a","b"]`, "杭州", "西湖大道"); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	// 行 2：指针列 NULL、时间零值串、json 空串（与"未设置"同形）。
	if _, err := base.ExecContext(tctx, ins,
		"李四", 0, nil, "0001-01-01 00:00:00", nil,
		"", "", ""); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
}

// R6 扫描语义：NULL→零值/nil、时间兼容、json 反序列化、投影子集。
func TestQueryAllScan(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)

	var out []*row
	out, err := QueryAll[row](tctx, db, &dbwm.Query{Table: "t"})
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(out))
	}
	r1 := out[0]
	if r1.ID != 1 || r1.Name != "张三" || r1.Age != 30 {
		t.Fatalf("行 1 基本列 = %+v", r1)
	}
	if r1.Nick == nil || *r1.Nick != "三少" {
		t.Fatalf("nick = %v，期望 *三少", r1.Nick)
	}
	if got := r1.Born.UTC().Format(time.RFC3339); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("born = %v，期望 2026-01-02T03:04:05Z", got)
	}
	if len(r1.Payload) != 2 || r1.Payload[1] != "b" {
		t.Fatalf("payload = %v，期望 [a b]", r1.Payload)
	}
	r2 := out[1]
	if r2.Nick != nil {
		t.Fatalf("NULL nick 应读出 nil，得到 %v", *r2.Nick)
	}
	if r2.Age != 0 {
		t.Fatalf("age = %d，期望 0", r2.Age)
	}
	if !r2.Born.IsZero() {
		t.Fatalf("零值时间串应读回零值，得到 %v", r2.Born)
	}
	if r2.Payload != nil {
		t.Fatalf("json 空串应还原零值，得到 %v", r2.Payload)
	}
}

// R2 投影：T 是表列子集；列清单由计划填入。
func TestQueryProjection(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)

	out, err := QueryAll[addr](tctx, db, &dbwm.Query{Table: "t"})
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(out) != 2 || out[0].AddrCity != "杭州" || out[1].AddrCity != "" {
		t.Fatalf("投影结果数 = %d，首行 city = %q（期望两行、行 1 为 杭州）", len(out), out[0].AddrCity)
	}
}

// R2.5 投影未声明任何 dbx 字段 → 报错。
type empty struct {
	Name string
}

func TestPlanEmpty(t *testing.T) {
	if _, err := PlanOf(&empty{}); err == nil {
		t.Fatal("没有任何 dbx 字段的投影应当报错")
	}
}

// R3 配合 query 包：条件树 → Where；QueryOne 未命中 (nil, nil)。
type cond struct {
	Name string `dbx:"name"`
}

func TestQueryOne(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)

	tpl, err := query.ParseWhere(&cond{})
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	w, err := tpl.Build(tctx, &cond{Name: "李四"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	one, err := QueryOne[row](tctx, db, &dbwm.Query{Table: "t", Where: w})
	if err != nil {
		t.Fatalf("QueryOne: %v", err)
	}
	if one == nil || one.Name != "李四" {
		t.Fatalf("命中行 = %+v", one)
	}

	w2, _ := tpl.Build(tctx, &cond{Name: "不存在"})
	miss, err := QueryOne[row](tctx, db, &dbwm.Query{Table: "t", Where: w2})
	if err != nil {
		t.Fatalf("QueryOne 未命中应无错误: %v", err)
	}
	if miss != nil {
		t.Fatalf("未命中应返回 nil，得到 %+v", miss)
	}
}

// R5 分页：总数 + 当页；配合 rows 的列清单注入。
func TestQueryPaged(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)

	total, page, err := QueryPaged[row](tctx, db, &dbwm.PagedQuery{
		Query:  dbwm.Query{Table: "t", Sorts: []string{"-id"}},
		Paging: dbwm.Paging{Page: 1, Size: 1},
	})
	if err != nil {
		t.Fatalf("QueryPaged: %v", err)
	}
	if total != 2 || len(page) != 1 || page[0].Name != "李四" {
		t.Fatalf("total=%d 行数=%d 首行=%v，期望 total=2、单页首行按 -id 是李四", total, len(page), page[0].Name)
	}
}

// R6 时间兼容：Unix 时间戳按 UTC 解析（数量级区分单位）。
func TestFlexTimeUnix(t *testing.T) {
	tm, ok, err := parseTimeString("1767236645")
	if err != nil || !ok {
		t.Fatalf("parseTimeString: %v ok=%v", err, ok)
	}
	if !tm.Equal(time.Unix(1767236645, 0).UTC()) {
		t.Fatalf("Unix 秒解析 = %v，期望 UTC 口径", tm)
	}
	n := int64(1767236645123) // 毫秒
	if tm := unixToTime(n); !tm.Equal(time.UnixMilli(n).UTC()) {
		t.Fatalf("Unix 毫秒解析 = %v", tm)
	}
}

// R6 嵌套路径上的 nil 指针子结构预分配。
type nested struct {
	Info *info `dbx:",inline"`
}
type info struct {
	Name string `dbx:"name"`
}

func TestNilPtrInline(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)
	out, err := QueryAll[nested](tctx, db, &dbwm.Query{Table: "t"})
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(out) != 2 || out[0].Info == nil || out[0].Info.Name != "张三" {
		t.Fatalf("嵌套结果 = %+v", out[0])
	}
}

// R6 自定义 sql.Scanner 类型：直扫，由实现自己解码。
type lowerName struct {
	Name string
}

// Scan 实现 sql.Scanner：把 TEXT 列转小写后写入字段。
func (n *lowerName) Scan(src any) error {
	s, ok := stringSrc(src)
	if !ok {
		return fmt.Errorf("lowerName: 不支持的源 %T", src)
	}
	n.Name = strings.ToLower(s)
	return nil
}

type scannerRow struct {
	Name lowerName `dbx:"name"`
}

func TestScannerField(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)
	out, err := QueryAll[scannerRow](tctx, db, &dbwm.Query{Table: "t"})
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(out) != 2 || out[0].Name.Name != "张三" || out[1].Name.Name != "李四" {
		t.Fatalf("Scanner 字段解码 = %+v", out)
	}
}

// R4.2 无命中返回空切片（len 0，非 nil）。
func TestQueryAllEmptySlice(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)
	out, err := QueryAll[row](tctx, db, &dbwm.Query{
		Table: "t",
		Where: &dbwm.Wnode{Items: []dbwm.Node{&dbwm.Wcond{Column: "id", Op: dbwm.OpGt, Arg: int64(99)}}},
	})
	if err != nil {
		t.Fatalf("QueryAll: %v", err)
	}
	if len(out) != 0 || out == nil {
		t.Fatalf("无命中应返回非 nil 空切片，得到 %#v", out)
	}
}

// 评审 F4：投影类型是指针 → 入口报错并指明写法。
func TestPointerProjectionRejected(t *testing.T) {
	db, base := openDB(t)
	seed(t, base)
	if _, err := QueryAll[*row](tctx, db, &dbwm.Query{Table: "t"}); err == nil {
		t.Fatal("指针型投影应当报错")
	}
	if _, err := QueryOne[*row](tctx, db, &dbwm.Query{Table: "t"}); err == nil {
		t.Fatal("指针型投影（QueryOne）应当报错")
	}
}
