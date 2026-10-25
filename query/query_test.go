package query

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"slyorm/dbwm"
	"slyorm/util"
)

// ctx 是 Build 的占位参数（核心算子是纯内存计算）。
var ctx = context.Background()

// leafEq 取出单叶子条件的 Op 与 Arg，便于断言。
func leafEq(t *testing.T, n dbwm.Node) (*dbwm.Wcond, *dbwm.Wnode) {
	t.Helper()
	switch v := n.(type) {
	case *dbwm.Wcond:
		return v, nil
	case *dbwm.Wnode:
		return nil, v
	}
	t.Fatalf("节点既不是叶子也不是组: %T", n)
	return nil, nil
}

// R1 字段参与：默认精确匹配（零值也参与），omit 才跳过。
type partQuery struct {
	ID   int     `dbx:"id"`
	Age  int     `dbx:"age,omit"`
	Nick *string `dbx:"nick"`
}

func TestParticipation(t *testing.T) {
	// 零值标量参与（id = 0 是条件）；omit 的零值跳过；nil 指针跳过。
	q := &partQuery{}
	w, err := ParseWhere(q)
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	node, err := w.Build(ctx, q)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if node == nil || len(node.Items) != 1 {
		t.Fatalf("期望只有 id 一条条件，得到 %+v", node)
	}
	c, _ := leafEq(t, node.Items[0])
	if c.Op != dbwm.OpEq || c.Arg != 0 {
		t.Fatalf("id 条件 = %+v，期望 eq/0", c)
	}

	// 非零值都参与。
	age := 30
	nick := "三"
	q = &partQuery{ID: 7, Age: age, Nick: &nick}
	node, _ = w.Build(ctx, q)
	if len(node.Items) != 3 {
		t.Fatalf("期望 3 条条件，得到 %d", len(node.Items))
	}
}

// R1 nil 指针 vs 指向零值：nil = 没有值；&"" = 显式赋过零值（参与）。
func TestNilPointerVsZeroPointer(t *testing.T) {
	q := &partQuery{Nick: new(string)} // &""
	w, _ := ParseWhere(q)
	node, err := w.Build(ctx, q)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, it := range node.Items {
		if c, _ := leafEq(t, it); c != nil && c.Column == "nick" {
			found = true
			if c.Arg != "" {
				t.Fatalf("nick 参数 = %v，期望空串", c.Arg)
			}
		}
	}
	if !found {
		t.Fatal("&\"\" 应当参与条件（显式赋过零值）")
	}
}

// R1 空切片没有值；非空切片参与（in 展开）。
type sliceQuery struct {
	IDs []int64 `dbx:"id"`
}

func TestSliceParticipation(t *testing.T) {
	w, _ := ParseWhere(&sliceQuery{})
	node, err := w.Build(ctx, &sliceQuery{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if node != nil {
		t.Fatalf("空切片不应生成条件，得到 %+v", node)
	}
	node, _ = w.Build(ctx, &sliceQuery{IDs: []int64{1, 2}})
	c, _ := leafEq(t, node.Items[0])
	if c.Op != dbwm.OpIn {
		t.Fatalf("切片推断算子 = %s，期望 in", c.Op)
	}
	args := c.Arg.([]any)
	if len(args) != 2 || args[0] != int64(1) {
		t.Fatalf("in 参数 = %v，期望 [1 2]", args)
	}
}

// R1 sql.Null*：无效没有值；有效参与（含内容为零）。
type nullQuery struct {
	Name sql.NullString `dbx:"name"`
}

func TestNullTypeParticipation(t *testing.T) {
	w, _ := ParseWhere(&nullQuery{})
	node, _ := w.Build(ctx, &nullQuery{})
	if node != nil {
		t.Fatal("Valid == false 不应生成条件")
	}
	node, err := w.Build(ctx, &nullQuery{Name: sql.NullString{String: "", Valid: true}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if node == nil {
		t.Fatal("Valid == true（内容为零）应当参与")
	}
}

// R1 isnull / notnull 是布尔开关：true 才加。
type nullFlagQuery struct {
	Del   bool  `dbx:"deleted,op=notnull"`
	Purge *bool `dbx:"purged,op=isnull"`
}

func TestNullSwitch(t *testing.T) {
	w, _ := ParseWhere(&nullFlagQuery{})
	node, _ := w.Build(ctx, &nullFlagQuery{})
	if node != nil {
		t.Fatal("false / nil 不应生成条件")
	}
	purge := true
	node, _ = w.Build(ctx, &nullFlagQuery{Purge: &purge})
	if len(node.Items) != 1 {
		t.Fatalf("期望只有 purge 一条，得到 %d 条", len(node.Items))
	}
	c, _ := leafEq(t, node.Items[0])
	if c.Op != dbwm.OpIsNull || c.Arg != nil {
		t.Fatalf("purge 条件 = %+v，期望 isnull 不绑值", c)
	}
}

// R2 列名推导：首段 > col= > snake_case；同列不同算子放行。
type nameQuery struct {
	From *time.Time `dbx:"created_at,op=gte"`
	To   *time.Time `dbx:"created_at,op=lte"`
	City string     `dbx:"col=city_name"`
}

func TestColumnName(t *testing.T) {
	w, err := ParseWhere(&nameQuery{})
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	cols := w.Columns()
	if len(cols) != 3 || cols[0] != "created_at" || cols[1] != "created_at" || cols[2] != "city_name" {
		t.Fatalf("模板列 = %v，期望 [created_at created_at city_name]", cols)
	}
}

// R3 条件组：comb=or 的括号由 Wnode 结构表达；空组消失。
type groupQuery struct {
	Status int `dbx:"status"`
	Any    struct {
		City string `dbx:"city,omit"`
		Tel  string `dbx:"tel,omit"`
	} `dbx:",inline,comb=or"`
}

func TestGroupOr(t *testing.T) {
	w, err := ParseWhere(&groupQuery{})
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	q := &groupQuery{Status: 1}
	q.Any.City = "杭州"
	q.Any.Tel = "138"
	node, err := w.Build(ctx, q)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(node.Items) != 2 {
		t.Fatalf("期望 2 个子节点，得到 %d", len(node.Items))
	}
	_, g := leafEq(t, node.Items[1])
	if g == nil || g.Comb != dbwm.CombOr || len(g.Items) != 2 {
		t.Fatalf("or 组 = %+v，期望 CombOr + 2 条", g)
	}

	// 组内全为零值 → 该组连同消失。
	q2 := &groupQuery{Status: 1}
	node, _ = w.Build(ctx, q2)
	if len(node.Items) != 1 {
		t.Fatalf("空组应消失，得到 %d 条", len(node.Items))
	}
}

// R3 not 组：组内按 and 连接后整体取反。
type notQuery struct {
	A string `dbx:"a"`
	N struct {
		B string `dbx:"b"`
		C string `dbx:"c"`
	} `dbx:",inline,comb=not"`
}

func TestGroupNot(t *testing.T) {
	w, _ := ParseWhere(&notQuery{})
	q := &notQuery{A: "x"}
	q.N.B = "1"
	q.N.C = "2"
	node, _ := w.Build(ctx, q)
	_, g := leafEq(t, node.Items[1])
	if g == nil || g.Comb != dbwm.CombNot || len(g.Items) != 2 {
		t.Fatalf("not 组 = %+v", g)
	}
}

// R4 like：没有 % 补成包含匹配；自带 % 原样保留。
type likeQuery struct {
	Name string `dbx:"name,op=like"`
}

func TestLikeWildcard(t *testing.T) {
	w, _ := ParseWhere(&likeQuery{})
	node, _ := w.Build(ctx, &likeQuery{Name: "张"})
	c, _ := leafEq(t, node.Items[0])
	if c.Arg != "%张%" {
		t.Fatalf("like 参数 = %v，期望 %%张%%", c.Arg)
	}
	node, _ = w.Build(ctx, &likeQuery{Name: "张%"})
	c, _ = leafEq(t, node.Items[0])
	if c.Arg != "张%" {
		t.Fatalf("自带 %% 的模式被改写了: %v", c.Arg)
	}
}

// R4 regex 是 like 的别名；未知算子报错；算子与类型不匹配报错。
type badOpQuery struct {
	Name string `dbx:"name,op=regex"`
}

func TestRegexAlias(t *testing.T) {
	if _, err := ParseWhere(&badOpQuery{Name: "x"}); err != nil {
		t.Fatalf("regex 应归一化为 like: %v", err)
	}
}

type unknownOpQuery struct {
	Name string `dbx:"name,op=xyz"`
}

func TestUnknownOp(t *testing.T) {
	_, err := ParseWhere(&unknownOpQuery{})
	if err == nil {
		t.Fatal("未知算子应当报错")
	}
}

type badTypeQuery struct {
	Age int `dbx:"age,op=like"`
}

func TestOpTypeMismatch(t *testing.T) {
	if _, err := ParseWhere(&badTypeQuery{}); err == nil {
		t.Fatal("op=like 配 int 字段应当报错")
	}
}

// R5 区间：左闭右开展开为 gte + lt 的 and 组；单端照常成组；两端皆零不参与。
type rangeQuery struct {
	CreatedAt *util.Range[int64] `dbx:"created_at"`
}

func TestRangeExpansion(t *testing.T) {
	w, _ := ParseWhere(&rangeQuery{})
	node, err := w.Build(ctx, &rangeQuery{CreatedAt: &util.Range[int64]{Min: 1, Max: 10}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, g := leafEq(t, node.Items[0])
	if g == nil || g.Comb != dbwm.CombAnd || len(g.Items) != 2 {
		t.Fatalf("区间组 = %+v，期望 and + gte/lt 两条", g)
	}
	lo, _ := leafEq(t, g.Items[0])
	hi, _ := leafEq(t, g.Items[1])
	if lo.Op != dbwm.OpGte || lo.Arg != int64(1) || hi.Op != dbwm.OpLt || hi.Arg != int64(10) {
		t.Fatalf("区间边界 = %+v / %+v，期望 gte 1 / lt 10", lo, hi)
	}

	// 只有下界。
	node, _ = w.Build(ctx, &rangeQuery{CreatedAt: &util.Range[int64]{Min: 1}})
	_, g = leafEq(t, node.Items[0])
	if len(g.Items) != 1 {
		t.Fatalf("单端区间应有 1 条，得到 %d", len(g.Items))
	}

	// 两端皆零：整条消失。
	node, _ = w.Build(ctx, &rangeQuery{})
	if node != nil {
		t.Fatal("两端皆零不应生成条件")
	}
}

// R6 没有任何生效条件 → nil。
func TestEmptyBuild(t *testing.T) {
	w, _ := ParseWhere(&partQuery{})
	node, err := w.Build(ctx, &partQuery{Age: 0})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if node == nil {
		t.Fatal("id = 0 是生效条件，不应为 nil")
	}
}

// R6.5 模板按类型缓存。
func TestTemplateCache(t *testing.T) {
	w1, _ := ParseWhere(&partQuery{})
	w2, _ := ParseWhere(partQuery{})
	if w1 != w2 {
		t.Fatal("同一类型应命中同一条模板缓存")
	}
}

// R8 自定义算子：归约为核心算子组合（prefix → like + 后缀 %）。
type prefixQuery struct {
	Name string `dbx:"name,op=prefix"`
}

func TestRegisterOperator(t *testing.T) {
	RegisterOperator("prefix", func(col string, v any) (*dbwm.Wcond, error) {
		return &dbwm.Wcond{Column: col, Op: dbwm.OpLike, Arg: v.(string) + "%"}, nil
	})
	w, err := ParseWhere(&prefixQuery{})
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	node, err := w.Build(ctx, &prefixQuery{Name: "张"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	c, _ := leafEq(t, node.Items[0])
	if c.Op != dbwm.OpLike || c.Arg != "张%" {
		t.Fatalf("prefix 条件 = %+v，期望 like 张%%", c)
	}
}

// 注册表纪律：算子重名（含内置算子）→ panic（评审 F8）。
func TestRegisterOperatorDup(t *testing.T) {
	RegisterOperator("dupop", func(string, any) (*dbwm.Wcond, error) { return nil, nil }) // 先占位
	for _, name := range []string{"eq", "dupop"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("重复注册算子 %q 应当 panic", name)
				}
			}()
			RegisterOperator(name, func(string, any) (*dbwm.Wcond, error) { return nil, nil })
		}()
	}
}

// 注册表纪律：自定义选项重名 → panic（评审 F8）。
func TestRegisterOptionDup(t *testing.T) {
	RegisterOption("dupopt", func(*Cond, string) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册选项应当 panic")
		}
	}()
	RegisterOption("dupopt", func(*Cond, string) error { return nil })
}
