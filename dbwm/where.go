package dbwm

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 条件树渲染
//
// 片段在内存里拼好后一次性写入语句 —— 参数与占位符因此严格同步。
// ---------------------------------------------------------------------------

// columnRef 解析条件里的一列，给出它的 SQL 引用形式。
type columnRef func(column string) (string, error)

// renderGroup 渲染一个条件组。
//
// 组的形态固定：
//   - and 组 → (a AND b)
//   - or 组  → (a OR b)
//   - not 组 → NOT (a AND b)
//
// top 为真表示这是顶层组（Query.Where / Query.Having 直接指向的组）：
// 顶层组不加外层括号，唯一的例外是 not 组 —— NOT (...) 的括号是 NOT 形态
// 的一部分。
//
// 组内子条件全都不存在时整个组不参与（连括号一起消失）。
func (r *renderer) renderGroup(g *Wnode, top bool, ref columnRef) (string, []any, error) {
	if g == nil {
		return "", nil, nil
	}
	var parts []string
	var args []any
	for _, item := range g.Items {
		frag, a, err := r.renderNode(item, ref)
		if err != nil {
			return "", nil, err
		}
		if frag == "" {
			continue
		}
		parts = append(parts, frag)
		args = append(args, a...)
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	comb := g.Comb
	if comb == "" {
		comb = CombAnd // 空串按 and 处理
	}
	var sep string
	switch comb {
	case CombAnd, CombNot:
		sep = " AND "
	case CombOr:
		sep = " OR "
	default:
		return "", nil, fmt.Errorf("dbwm: 未知的条件连接方式 %q（只能是 and/or/not）", comb)
	}
	joined := strings.Join(parts, sep)
	switch {
	case comb == CombNot:
		return "NOT (" + joined + ")", args, nil
	case top:
		return joined, args, nil
	default:
		return "(" + joined + ")", args, nil
	}
}

// renderNode 渲染条件树的一个节点：叶子走算子表，子组递归 —— Composite
// 的分发点。
//
// 节点种类由接口密封，"既没有条件也没有条件组""同时给了条件与条件组"
// 在类型上不可表达；剩下的非法形态只有 nil（nil 接口或 nil 指针），
// 渲染期报错。
func (r *renderer) renderNode(n Node, ref columnRef) (string, []any, error) {
	switch n := n.(type) {
	case *Wcond:
		if n == nil {
			return "", nil, fmt.Errorf("dbwm: 条件节点不能是空指针")
		}
		return r.renderCond(n, ref)
	case *Wnode:
		if n == nil {
			return "", nil, fmt.Errorf("dbwm: 条件节点不能是空指针")
		}
		return r.renderGroup(n, false, ref)
	case nil:
		return "", nil, fmt.Errorf("dbwm: 条件节点不能是 nil")
	default:
		return "", nil, fmt.Errorf("dbwm: 未知条件节点类型 %T", n)
	}
}

// renderCond 渲染一条叶子条件。
//
// 算子未注册、值形态不对（如 in 给了标量）在这里或算子实现里报错 ——
// 渲染期就该拦住，不把错位的 SQL 交给驱动。
func (r *renderer) renderCond(c *Wcond, ref columnRef) (string, []any, error) {
	spec, ok := lookupOp(string(c.Op))
	if !ok {
		return "", nil, fmt.Errorf("dbwm: 未注册的算子 %q", c.Op)
	}
	left, err := ref(c.Column)
	if err != nil {
		return "", nil, err
	}
	// 算子自己分配占位符：分配数必须与它返回的参数个数一致。
	before := r.idxPlaceholder
	frag, args, err := spec.render(r, c, left)
	if err != nil {
		return "", nil, fmt.Errorf("dbwm: 算子 %q 渲染失败: %w", c.Op, err)
	}
	if n := r.idxPlaceholder - before; n != len(args) {
		return "", nil, fmt.Errorf("dbwm: 算子 %q 的片段里占位符数（%d）与绑定参数数（%d）不一致: %s",
			c.Op, n, len(args), frag)
	}
	return frag, args, nil
}

// whereRef 解析普通条件里的列：引号包裹的标识符。
//
// 标识符不做本地校验 —— 方言的引用会转义引号（注入面封死），写错的列名
// 由数据库报错暴露（错误带语句文本，见 R6.1）。
func (r *renderer) whereRef() columnRef {
	return func(column string) (string, error) {
		return r.quoteIdent(column), nil
	}
}
