package query

import (
	"context"
	"fmt"
	"reflect"

	"github.com/icdb37/slyorm/dbwm"
	"github.com/icdb37/slyorm/util"
)

// ---------------------------------------------------------------------------
// 运行期取值与条件树生成
// ---------------------------------------------------------------------------

// buildGroup 取值并组装一个条件组，产出 dbwm 条件树节点。
//
// 组内按 comb 连接（and / or / not）；没有任何生效子条件时返回 nil
// —— 空组不参与（prd-select R6.3）。
func buildGroup(ctx context.Context, g *group, elem reflect.Value) (*dbwm.Wnode, error) {
	node := &dbwm.Wnode{Comb: dbwmComb(g.comb)}
	for _, item := range g.items {
		child, err := buildNode(ctx, item, elem)
		if err != nil {
			return nil, err
		}
		if child == nil {
			continue
		}
		node.Items = append(node.Items, child)
	}
	if len(node.Items) == 0 {
		return nil, nil
	}
	return node, nil
}

// buildNode 取值并组装一个条件节点；返回 nil 表示"这条条件不参与"。
//
// 区间（range）在这里结构化展开为 gte + lt 的 and 组 —— dbwm 不设 range 算子
// （prd-dbwm §4.1），括号由 Wnode 的天然属性保证，放进 or 组不会被拆散。
func buildNode(ctx context.Context, n *node, elem reflect.Value) (dbwm.Node, error) {
	if n.group != nil {
		g, err := buildGroup(ctx, n.group, elem)
		if err != nil {
			return nil, err
		}
		if g == nil {
			return nil, nil // 空组不参与：不能把 nil 指针装进接口（!= nil）
		}
		return g, nil
	}
	c := n.leaf.Cond
	val, ok := leafValue(elem, n.leaf)
	if !ok {
		return nil, nil
	}
	if n.leaf.kind == kindRange {
		return rangeGroup(c.Column, val), nil
	}
	op, found := lookupOperator(c.Op)
	if !found {
		return nil, fmt.Errorf("query: 算子 %q 未注册", c.Op)
	}
	cond, err := op(c.Column, val)
	if err != nil {
		return nil, fmt.Errorf("query: 算子 %s 生成条件失败: %w", c.Op, err)
	}
	if cond == nil {
		return nil, nil
	}
	return cond, nil
}

// rangeGroup 把区间展开为 `col >= Min AND col < Max` 的 and 组（左闭右开）。
//
// 只生成给了的那端；单端也照常成组（括号形态不随数据变化，prd-select R5/R6）。
// 走到这里的区间必然至少给了一端（两端皆零在 leafValue 里已判为不参与）。
func rangeGroup(col string, v any) *dbwm.Wnode {
	iv := v.(util.Interval)
	g := &dbwm.Wnode{Comb: dbwm.CombAnd}
	if iv.HasMin() {
		g.Items = append(g.Items, &dbwm.Wcond{Column: col, Op: dbwm.OpGte, Arg: iv.MinValue()})
	}
	if iv.HasMax() {
		g.Items = append(g.Items, &dbwm.Wcond{Column: col, Op: dbwm.OpLt, Arg: iv.MaxValue()})
	}
	return g
}

// dbwmComb 把读规则的组合方式映射到 dbwm 的 Comb。
func dbwmComb(comb string) dbwm.Comb {
	switch comb {
	case "or":
		return dbwm.CombOr
	case "not":
		return dbwm.CombNot
	default:
		return dbwm.CombAnd // 空串按 and（与 prd-dbwm §4.3 一致）
	}
}

// leafValue 取出叶子字段的值，并报告"有没有值"。
//
// "没有值"的四种天然形态（见 prd-select R1.3）：指针为 nil、切片/数组为空、
// sql.Null* 无效、区间两端皆零。
func leafValue(elem reflect.Value, l *leaf) (any, bool) {
	rv, ok := util.ValueAt(elem, l.Path)
	if !ok {
		return nil, false // 路径上有 nil 指针
	}
	ev := util.DerefValue(rv)
	if !ev.IsValid() {
		return nil, false // 指针本身是 nil
	}
	switch l.kind {
	case kindRange:
		iv, ok := ev.Interface().(util.Interval)
		if !ok {
			return nil, false
		}
		if !iv.HasMin() && !iv.HasMax() {
			return nil, false // 两端皆零：整个区间不参与
		}
		return iv, true
	case kindNull:
		v, valid, ok := util.NullValue(ev)
		if !ok {
			return nil, false
		}
		if !valid {
			return nil, false // Valid == false：没有值
		}
		if l.Omit && util.IsZero(reflect.ValueOf(v)) {
			return nil, false
		}
		return v, true
	case kindSlice, kindBytes:
		if ev.Len() == 0 {
			return nil, false // 空切片 / 空字节串：没有值
		}
		return ev.Interface(), true
	default: // kindScalar / kindBool
		if l.Omit && util.IsZero(ev) {
			return nil, false // omit：零值当成没有值
		}
		return util.Value(ev), true
	}
}
