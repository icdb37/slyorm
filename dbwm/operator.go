package dbwm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// 算子
// ---------------------------------------------------------------------------

// Operator 是一个比较算子：给方言与条件，产出 SQL 片段与绑定参数。
//
// 条件的值在 Wcond.Arg（一个值）；算子决定它怎么参与比较 —— 返回的参数
// 列表（片段里每个占位符一个）由算子自己成形。
//
// 片段里的占位符一律写 ? —— 注册时 dbwm 把它换成渲染器分配的方言占位符
// （PostgreSQL 的 $1、$2…）。片段里的 ? 数必须与返回的参数个数一致，
// 否则渲染期报错。
//
// 引号由算子自己加（QuoteIdent(d, c.Column)）—— 方言只给引用字符，
// 加引号与转义由 dbwm 统一做。
type Operator func(d Dialect, c *Wcond) (string, []any, error)

// opSpec 是算子的内部描述。
//
// render 的第三个参数 ref 是左侧列的 SQL 引用形式：普通列是引号包裹的
// 标识符，HAVING 里的聚合别名则改写成聚合表达式。
//
// render 拿渲染器：占位符由渲染器分配，编号在整条语句里连续。
type opSpec struct {
	// render 渲染条件。
	render func(r *renderer, c *Wcond, ref string) (string, []any, error)
}

// coreOps 是内置算子表。
//
// 值形态（Wcond.Arg）：eq/ne/gt/gte/lt/lte/like 是标量；in/nin 是集合
// （任意切片，至少 1 个元素）；isnull/notnull 忽略。
var coreOps = map[string]opSpec{
	"eq":      {render: cmpOp("=")},
	"ne":      {render: cmpOp("<>")},
	"gt":      {render: cmpOp(">")},
	"gte":     {render: cmpOp(">=")},
	"lt":      {render: cmpOp("<")},
	"lte":     {render: cmpOp("<=")},
	"like":    {render: cmpOp("LIKE")},
	"in":      {render: inOp("IN")},
	"nin":     {render: inOp("NOT IN")},
	"isnull":  {render: nullOp("")},
	"notnull": {render: nullOp("NOT")},
}

var (
	opMu      sync.RWMutex
	customOps = map[string]opSpec{}
)

// RegisterOperator 注册一个自定义算子。
//
// 算子名不可覆盖：内置算子与已注册的算子都不可被覆盖 —— 全局表被悄悄
// 改写 eq 的语义没法排查。注册必须在该算子第一次被使用之前完成。
//
//	dbwm.RegisterOperator("contains", func(d dbwm.Dialect, c *dbwm.Wcond) (string, []any, error) {
//		return dbwm.QuoteIdent(d, c.Column) + " LIKE ?", []any{"%" + fmt.Sprint(c.Arg) + "%"}, nil
//	})
func RegisterOperator(name string, op Operator) error {
	if name == "" {
		return fmt.Errorf("dbwm: 算子名不能为空")
	}
	if op == nil {
		return fmt.Errorf("dbwm: 算子 %q 的实现不能为 nil", name)
	}
	opMu.Lock()
	defer opMu.Unlock()
	if _, ok := coreOps[name]; ok {
		return fmt.Errorf("dbwm: 算子 %q 是内置算子，不可覆盖", name)
	}
	if _, ok := customOps[name]; ok {
		return fmt.Errorf("dbwm: 算子 %q 已注册，不可覆盖", name)
	}
	customOps[name] = opSpec{
		render: func(r *renderer, c *Wcond, ref string) (string, []any, error) {
			frag, args, err := op(r.d, c)
			if err != nil {
				return "", nil, err
			}
			n := strings.Count(frag, "?")
			if n != len(args) {
				return "", nil, fmt.Errorf(
					"dbwm: 自定义算子 %q 的片段里占位符数（%d）与绑定参数数（%d）不一致: %s",
					name, n, len(args), frag)
			}
			// 自定义算子自己给列加引号，因此左引用通常就是引号包裹的列名。
			// HAVING 里引用聚合别名时左引用是聚合表达式，需要把片段开头的
			// 列名换掉 —— 换不掉就报错，不产出半对的 SQL。
			prefix := QuoteIdent(r.d, c.Column)
			if ref != prefix {
				if !strings.HasPrefix(frag, prefix) {
					return "", nil, fmt.Errorf(
						"dbwm: 自定义算子 %q 的片段未以列名 %q 开头，无法改写成聚合表达式", name, c.Column)
				}
				frag = ref + frag[len(prefix):]
			}
			// 片段里的 ? 换成渲染器分配的占位符：编号在整条语句里连续。
			for i := 0; i < n; i++ {
				frag = strings.Replace(frag, "?", r.nextPlaceholder(), 1)
			}
			return frag, args, nil
		},
	}
	return nil
}

// lookupOp 查找算子：先找自定义表，再找内置表。
func lookupOp(name string) (opSpec, bool) {
	opMu.RLock()
	defer opMu.RUnlock()
	if s, ok := customOps[name]; ok {
		return s, true
	}
	s, ok := coreOps[name]
	return s, ok
}

// cmpOp 是"列 关键字 值"型算子的实现。
func cmpOp(keyword string) func(*renderer, *Wcond, string) (string, []any, error) {
	return func(r *renderer, c *Wcond, ref string) (string, []any, error) {
		return ref + " " + keyword + " " + r.nextPlaceholder(), []any{c.Arg}, nil
	}
}

// inOp 是 IN / NOT IN 型算子的实现：集合值展开成 n 个绑定参数。
func inOp(keyword string) func(*renderer, *Wcond, string) (string, []any, error) {
	return func(r *renderer, c *Wcond, ref string) (string, []any, error) {
		args, err := spread(c.Arg)
		if err != nil {
			return "", nil, fmt.Errorf("dbwm: 算子 %q: %w", c.Op, err)
		}
		holders := make([]string, len(args))
		for i := range holders {
			holders[i] = r.nextPlaceholder()
		}
		return ref + " " + keyword + " (" + strings.Join(holders, ",") + ")", args, nil
	}
}

// nullOp 是 IS NULL / IS NOT NULL 型算子的实现（不绑参数）。
func nullOp(keyword string) func(*renderer, *Wcond, string) (string, []any, error) {
	return func(_ *renderer, c *Wcond, ref string) (string, []any, error) {
		if keyword == "" {
			return ref + " IS NULL", nil, nil
		}
		return ref + " IS NOT NULL", nil, nil
	}
}

// spread 把集合值展开成绑定参数列表：支持任意切片类型
// （[]any、[]string、[]int…）。空集合（含 nil）报错 —— 空集合应不参与
// 条件（值成形是上层的职责，dbwm 收到空的兜底报错，不产出非法的 IN ()）。
func spread(v any) ([]any, error) {
	bad := func() error {
		return fmt.Errorf("in/nin 的值必须是集合（切片），实际是 %T", v)
	}
	if v == nil {
		return nil, bad()
	}
	if s, ok := v.([]any); ok {
		if len(s) == 0 {
			return nil, fmt.Errorf("in/nin 的集合不能为空（空集合应不参与条件）")
		}
		return s, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, bad()
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("in/nin 的集合不能为空（空集合应不参与条件）")
	}
	return out, nil
}
