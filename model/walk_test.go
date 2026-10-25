package model

import (
	"testing"
	"time"
)

// R2 列名推导：首段 / col= / snake_case、inline 前缀链、json 单列、未标注忽略。
type Address struct {
	City   string `dbx:"city"`
	Street string `dbx:"street"`
}

type person struct {
	ID        int64      `dbx:"id,autopk"`
	Name      string     `dbx:"name"`
	Age       int        `dbx:"age"`
	Address   Address    `dbx:"addr_,inline"`
	Extra     Extra      `dbx:"-"` // 整组忽略
	Remark    string     // 未标注 dbx 忽略
	CreatedAt time.Time  `dbx:"created_at"`
	UpdatedAt *time.Time `dbx:"updated_at"`
}

type Extra struct {
	Note string `dbx:"note"`
}

func TestWalkColumns(t *testing.T) {
	m, err := Parse(&person{}) // 指针也认，缓存按解指针后的类型
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	got := m.Names()
	want := []string{"id", "name", "age", "addr_city", "addr_street", "created_at", "updated_at"}
	if len(got) != len(want) {
		t.Fatalf("列集合 =\n%v\n期望\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("列集合 =\n%v\n期望\n%v", got, want)
		}
	}
}

// R2 inline 无前缀：子列沿用自身列名；前缀末尾下划线可写可不写。
type personNoPrefix struct {
	ID      int64   `dbx:"id,autopk"`
	Address Address `dbx:",inline"`
}

func TestWalkInlineNoPrefix(t *testing.T) {
	m, err := Parse(personNoPrefix{})
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	got := m.Names()
	if got[1] != "city" || got[2] != "street" {
		t.Fatalf("无前缀展开的列名 = %v，期望 city/street 不带前缀", got)
	}
}

// R2 多级前缀链逐级累加。
type inner struct {
	Code string `dbx:"code"`
}
type outer struct {
	Inner inner `dbx:"in_,inline"`
}
type multi struct {
	Outer outer `dbx:"out_,inline"`
}

func TestWalkPrefixChain(t *testing.T) {
	m, err := Parse(multi{})
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	if got := m.Names(); got[0] != "out_in_code" {
		t.Fatalf("多级前缀列名 = %v，期望 out_in_code", got)
	}
}

// R2 主键标记：pk 列与 autopk 列都进 PKNames；多个 autopk 报错。
type withPK struct {
	OrderID int64 `dbx:"order_id,pk"`
	ItemID  int64 `dbx:"item_id,pk"`
}

func TestPKNames(t *testing.T) {
	m, err := Parse(withPK{})
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	got := m.PKNames()
	if len(got) != 2 || got[0] != "order_id" || got[1] != "item_id" {
		t.Fatalf("主键列 = %v，期望 [order_id item_id]", got)
	}
}

type dupAutoPK struct {
	A int64 `dbx:"a,autopk"`
	B int64 `dbx:"b,autopk"`
}

func TestMultipleAutoPK(t *testing.T) {
	if _, err := Parse(dupAutoPK{}); err == nil {
		t.Fatal("多个 autopk 应当报错")
	}
}

// R2 错误形态：列名重复、嵌套结构误用选项、没有任何 dbx 字段。
type dupCol struct {
	A string `dbx:"x"`
	B string `dbx:"x"`
}

func TestDuplicateColumn(t *testing.T) {
	if _, err := Parse(dupCol{}); err == nil {
		t.Fatal("列名重复应当报错")
	}
}

type nestedBadOpt struct {
	Address Address `dbx:"addr_,inline,null"`
}

func TestNestedBadOption(t *testing.T) {
	if _, err := Parse(nestedBadOpt{}); err == nil {
		t.Fatal("嵌套结构上的非法选项应当报错")
	}
}

type noDbx struct {
	Name string
}

func TestNoDbxFields(t *testing.T) {
	if _, err := Parse(noDbx{}); err == nil {
		t.Fatal("没有任何 dbx 字段应当报错")
	}
}

// prd-insert R11.5：json 与 inline 互斥，解析期报错（model 层统一拦截，
// 读写两侧不再各自判一遍 —— 评审 F2）。
type jsonInline struct {
	A map[string]any `dbx:"a,json,inline"`
}

func TestJSONInlineConflict(t *testing.T) {
	if _, err := Parse(jsonInline{}); err == nil {
		t.Fatal("json+inline 同时写应当在解析期报错")
	}
}

// inline 只能用在结构体字段上：标量字段带 inline → 解析期报错。
type leafInline struct {
	Name string `dbx:"name,inline"`
}

func TestLeafInline(t *testing.T) {
	if _, err := Parse(leafInline{}); err == nil {
		t.Fatal("标量字段带 inline 应当在解析期报错")
	}
}

// R1/R2 缓存：*User 与 User 是同一条；解析失败不缓存。
func TestParseCache(t *testing.T) {
	m1, err := Parse(personNoPrefix{})
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	m2, err := Parse(&personNoPrefix{})
	if err != nil {
		t.Fatalf("Parse 出错: %v", err)
	}
	if m1 != m2 {
		t.Fatal("同一类型（值形态与指针形态）应命中同一条缓存")
	}
}
