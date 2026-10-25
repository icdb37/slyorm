// Package query 把条件结构体翻译成 dbwm 的条件树（Wnode）。
//
// 需求与规则见 docs/prd-select.md；本包产出 IR 而不是 SQL 片段（见 prd-slyorm §7）：
// **值成形在这里**（like 补 %、区间展开、空集合跳过），**语法在 dbwm**
// （算子关键字、引号、占位符）。本包因此不需要方言。
//
// 一个结构体 + 一个 dbx 标签描述一组查询条件：
//
//	type PersonQuery struct {
//		ID        int64                  `dbx:"id"`            // 精确匹配：零值也参与
//		Name      string                 `dbx:"name,op=like"`  // 包含匹配
//		Age       int                    `dbx:"age,omit"`      // 零值当没有值
//		CreatedAt *util.Range[time.Time] `dbx:"created_at"`    // 左闭右开区间 → gte + lt 组
//		Any       struct {              // 条件组
//			City string `dbx:"city"`
//			Tel  string `dbx:"tel"`
//		} `dbx:",inline,comb=or"`
//	}
//
//	tpl, err := query.ParseWhere(&q)
//	w, err := tpl.Build(ctx, &q) // *dbwm.Wnode；没有任何生效条件时为 nil
//
// 没有任何生效条件时 Build 返回 nil —— 调用方据此不加 WHERE（或按业务规则报错）。
//
// 读规则与写规则是**两套互不干涉的规则集**，共用一个标签键 dbx：
// 写规则见 insert（docs/prd-insert.md）。
//
// 文件分工：
//
//	query.go    本文件 —— 对外 API（ParseWhere / Template）
//	option.go   读规则的选项名与校验
//	operator.go 算子表、算子注册、按类型推断与校验（值成形归这里）
//	parse.go    条件结构体 → 条件树模板（解析期，按类型缓存）
//	where.go    运行期取值 + 条件树生成（Wnode 组装、区间展开）
package query

import (
	"context"
	"fmt"
	"reflect"

	"github.com/icdb37/slyorm/dbwm"
)

// ---------------------------------------------------------------------------
// 对外 API
// ---------------------------------------------------------------------------

// Template 是条件树模板 —— 条件结构体解析后的静态结构（不含值）。
//
// 模板按类型缓存：同一个类型多次解析返回同一个模板。
type Template struct {
	root *group
	typ  reflect.Type
}

// ParseWhere 解析条件结构体，得到条件树模板。
//
// model 是条件结构体的指针（结构体值也行）；只看**类型**，与值无关。
// 解析期会校验标签：算子是否存在、算子与字段类型是否匹配、组合方式是否合法
// （见 prd-select R7）。
func ParseWhere(model any) (*Template, error) {
	t, err := modelType(model)
	if err != nil {
		return nil, err
	}
	if v, ok := templateCache.Load(t); ok {
		return v.(*Template), nil
	}
	tpl, err := parseTemplate(t)
	if err != nil {
		return nil, err
	}
	templateCache.Store(t, tpl)
	return tpl, nil
}

// Columns 返回模板里出现过的全部列名（含本次取值可能不参与的）。
//
// 供上层（Collection）对着表模型的列集合做存在性校验 —— 校验在模板层做，
// 与值无关，一次校验覆盖所有调用。
func (t *Template) Columns() []string {
	var out []string
	var walk func(g *group)
	walk = func(g *group) {
		for _, n := range g.items {
			if n.group != nil {
				walk(n.group)
				continue
			}
			out = append(out, n.leaf.Column)
		}
	}
	walk(t.root)
	return out
}

// Build 用给定的条件值生成 dbwm 条件树。
//
// 模板可以复用：解析一次，多次生成。没有任何生效条件时返回 nil（prd-select R6.3）
// —— 组内条件顺序即参数顺序，占位符编号由 dbwm 渲染时统一分配。
func (t *Template) Build(ctx context.Context, model any) (*dbwm.Wnode, error) {
	elem, err := modelValue(model)
	if err != nil {
		return nil, err
	}
	if elem.Type() != t.typ {
		return nil, fmt.Errorf("query: 模板是 %s 的，不能用在 %s 上", t.typ, elem.Type())
	}
	return buildGroup(ctx, t.root, elem)
}

// ---------------------------------------------------------------------------
// 入参校验
// ---------------------------------------------------------------------------

// modelType 取条件结构体的类型：指针与结构体值都认，nil 指针也认（解析只看类型）。
func modelType(model any) (reflect.Type, error) {
	if model == nil {
		return nil, fmt.Errorf("query: 条件模型不能为 nil")
	}
	t := reflect.TypeOf(model)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("query: 条件模型必须是结构体或结构体指针，实际是 %s", t.Kind())
	}
	return t, nil
}

// modelValue 取条件结构体的值：指针解引用成结构体值。
//
// nil 指针解析不出任何条件 —— 由调用方决定这是"无条件"还是错误
// （门面的 Get 对空条件报错、Query 视为查全表，见 prd-slyorm R3/R4）。
func modelValue(model any) (reflect.Value, error) {
	if model == nil {
		return reflect.Value{}, fmt.Errorf("query: 条件模型不能是 nil")
	}
	rv := reflect.ValueOf(model)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("query: 条件模型不能是 nil 指针")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("query: 条件模型必须是结构体或结构体指针，实际是 %s", rv.Kind())
	}
	return rv, nil
}
