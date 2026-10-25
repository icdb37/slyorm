package query

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"slyorm/dbwm"
	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 算子（值成形）
//
// 算子的职责是"语义 + 值成形"：把字段值补成可直接比较的形态
// （like 补 %、集合展开、布尔开关），产出 **dbwm 的核心算子叶子**（*dbwm.Wcond）。
// 交给 dbwm 的永远是核心算子 —— 自定义算子也只能归约为核心算子组合
// （见 prd-slyorm R13.5），语法与方言归 dbwm。
// ---------------------------------------------------------------------------

// Operator 是一个比较算子：给原始列名与取值，产出一条核心算子条件。
//
// 返回 nil（且无错误）表示"这条条件不参与"（如 isnull 的开关是 false）。
type Operator func(col string, v any) (*dbwm.Wcond, error)

var (
	operatorMu sync.RWMutex
	operators  = map[string]Operator{}
)

// RegisterOperator 注册一个算子，供 op=<名字> 使用。
//
// 算子定义为**语义 + 值成形**并归约为核心算子组合（不拼 SQL 片段）：
//
//	query.RegisterOperator("prefix", func(col string, v any) (*dbwm.Wcond, error) {
//		return &dbwm.Wcond{Column: col, Op: dbwm.OpLike, Arg: fmt.Sprint(v) + "%"}, nil
//	})
//
// 算子名不可覆盖：核心算子（eq/ne/…）与已注册的算子重名直接 panic ——
// 全局表被悄悄改写 `eq` 的语义没法排查（注册表纪律，见 rules/code.md §4.3）。
// 注册必须在该条件模型**第一次被解析**之前完成 —— 算子在解析期校验。
func RegisterOperator(name string, fn Operator) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		panic("query: 算子名不能为空")
	}
	if fn == nil {
		panic("query: 算子实现不能为 nil")
	}
	operatorMu.Lock()
	defer operatorMu.Unlock()
	if _, dup := operators[name]; dup {
		panic(fmt.Sprintf("query: 算子 %q 已注册，不可覆盖", name))
	}
	operators[name] = fn
}

// lookupOperator 查找算子。
func lookupOperator(name string) (Operator, bool) {
	operatorMu.RLock()
	defer operatorMu.RUnlock()
	fn, ok := operators[name]
	return fn, ok
}

// operatorNames 返回已注册的算子名（排序后），用于错误信息。
func operatorNames() []string {
	operatorMu.RLock()
	defer operatorMu.RUnlock()
	names := make([]string, 0, len(operators))
	for k := range operators {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// 核心算子表。
func init() {
	RegisterOperator("eq", opEq)
	RegisterOperator("ne", opNe)
	RegisterOperator("gt", opGt)
	RegisterOperator("gte", opGte)
	RegisterOperator("lt", opLt)
	RegisterOperator("lte", opLte)
	RegisterOperator("like", opLike)
	RegisterOperator("in", opIn)
	RegisterOperator("nin", opNin)
	RegisterOperator("isnull", opIsNull)
	RegisterOperator("notnull", opNotNull)
}

// opEq 产出 `col = v`。
func opEq(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpEq, Arg: v}, nil
}

// opNe 产出 `col <> v`。
func opNe(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpNe, Arg: v}, nil
}

// opGt 产出 `col > v`。
func opGt(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpGt, Arg: v}, nil
}

// opGte 产出 `col >= v`。
func opGte(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpGte, Arg: v}, nil
}

// opLt 产出 `col < v`。
func opLt(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpLt, Arg: v}, nil
}

// opLte 产出 `col <= v`。
func opLte(col string, v any) (*dbwm.Wcond, error) {
	return &dbwm.Wcond{Column: col, Op: dbwm.OpLte, Arg: v}, nil
}

// opLike 产出 `col LIKE 模式`。
//
// 值里**没有 %** 时补成 `%值%`（包含匹配）；已带 % 的原样保留 ——
// 调用方自己写的模式不被改写（prd-select R4）。
func opLike(col string, v any) (*dbwm.Wcond, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.String {
		return nil, fmt.Errorf("op=like 需要字符串值，实际是 %T", v)
	}
	s := rv.String()
	if !strings.Contains(s, "%") {
		s = "%" + s + "%"
	}
	return &dbwm.Wcond{Column: col, Op: dbwm.OpLike, Arg: s}, nil
}

// opIn 产出 `col IN (集合)`。空集合不生成条件（不会出现 IN ()）。
func opIn(col string, v any) (*dbwm.Wcond, error) {
	args, ok := sliceArgs(v)
	if !ok {
		return nil, fmt.Errorf("op=in 需要切片值，实际是 %T", v)
	}
	if len(args) == 0 {
		return nil, nil
	}
	return &dbwm.Wcond{Column: col, Op: dbwm.OpIn, Arg: args}, nil
}

// opNin 产出 `col NOT IN (集合)`。空集合不生成条件。
func opNin(col string, v any) (*dbwm.Wcond, error) {
	args, ok := sliceArgs(v)
	if !ok {
		return nil, fmt.Errorf("op=nin 需要切片值，实际是 %T", v)
	}
	if len(args) == 0 {
		return nil, nil
	}
	return &dbwm.Wcond{Column: col, Op: dbwm.OpNotIn, Arg: args}, nil
}

// opIsNull 产出 `col IS NULL`。
//
// 是**布尔开关**：值为 true 才加条件，false 返回 nil（不参与）；不绑参数。
func opIsNull(col string, v any) (*dbwm.Wcond, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("op=isnull 需要布尔值，实际是 %T", v)
	}
	if !b {
		return nil, nil
	}
	return &dbwm.Wcond{Column: col, Op: dbwm.OpIsNull}, nil
}

// opNotNull 产出 `col IS NOT NULL`（布尔开关，同 opIsNull）。
func opNotNull(col string, v any) (*dbwm.Wcond, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("op=notnull 需要布尔值，实际是 %T", v)
	}
	if !b {
		return nil, nil
	}
	return &dbwm.Wcond{Column: col, Op: dbwm.OpNotNull}, nil
}

// sliceArgs 把切片值拆成绑定参数序列。
func sliceArgs(v any) ([]any, bool) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	args := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		args[i] = rv.Index(i).Interface()
	}
	return args, true
}

// ---------------------------------------------------------------------------
// 字段形态与算子的匹配
// ---------------------------------------------------------------------------

// leafKind 描述叶子字段的形态 —— 决定算子怎么选、怎么校验。
type leafKind int

const (
	// kindScalar 普通标量（数字、字符串、时间…）。
	kindScalar leafKind = iota
	// kindSlice 切片 / 数组（[]byte 除外）。
	kindSlice
	// kindBytes 字节串（[]byte），按整体比较。
	kindBytes
	// kindNull sql.Null* 家族。
	kindNull
	// kindRange 区间类型 Range[T]。
	kindRange
	// kindBool 布尔字段（isnull / notnull 的开关）。
	kindBool
)

// intervalType 是区间类型的接口，Range[T] 实现它（见 util.Range）。
var intervalType = reflect.TypeOf((*util.Interval)(nil)).Elem()

// classify 判断字段类型（解引用后）的形态。
func classify(t reflect.Type) leafKind {
	base := util.DerefType(t)
	if base.Kind() == reflect.Struct {
		if base.Implements(intervalType) || reflect.PointerTo(base).Implements(intervalType) {
			return kindRange
		}
		if util.IsNullType(base) {
			return kindNull
		}
	}
	switch base.Kind() {
	case reflect.Slice:
		if base.Elem().Kind() == reflect.Uint8 {
			return kindBytes
		}
		return kindSlice
	case reflect.Array:
		return kindSlice
	case reflect.Bool:
		return kindBool
	}
	return kindScalar
}

// inferOp 按字段形态推断算子（op= 省略时用）。range 由 where.go 结构化展开，
// 不走算子表，但推断结果仍是 "range"。
func inferOp(k leafKind) string {
	switch k {
	case kindRange:
		return "range"
	case kindSlice:
		return "in"
	default:
		return "eq"
	}
}

// validateOp 校验算子与字段形态是否匹配（见 prd-select R7）。
func validateOp(op string, k leafKind, t reflect.Type) error {
	if k == kindRange && op != "range" {
		return fmt.Errorf("range 字段不能配 op=%s（区间用 op=range）", op)
	}
	switch op {
	case "eq":
		if k == kindSlice {
			return fmt.Errorf("op=eq 不支持切片字段")
		}
	case "ne", "gt", "gte", "lt", "lte":
		if k == kindSlice {
			return fmt.Errorf("op=%s 不支持切片字段", op)
		}
	case "like":
		if !isStringField(t) {
			return fmt.Errorf("op=like 需要字符串字段")
		}
	case "in", "nin":
		if k == kindBytes {
			return fmt.Errorf("op=%s 不支持 []byte（它按 eq 处理）", op)
		}
		if k != kindSlice {
			return fmt.Errorf("op=%s 需要切片字段", op)
		}
	case "isnull", "notnull":
		if k != kindBool {
			return fmt.Errorf("op=%s 需要布尔字段（true 才加条件）", op)
		}
	case "range":
		if k != kindRange {
			return fmt.Errorf("op=range 需要 Range 字段")
		}
	}
	return nil
}

// isStringField 判断字段是不是字符串（含 *string 与 sql.NullString）。
func isStringField(t reflect.Type) bool {
	base := util.DerefType(t)
	if base.Kind() == reflect.String {
		return true
	}
	if vt := util.NullValueType(base); vt != nil {
		return vt.Kind() == reflect.String
	}
	return false
}
