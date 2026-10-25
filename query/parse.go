package query

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 条件树模板
// ---------------------------------------------------------------------------

// Cond 是一个叶子条件的元信息。
//
// 业务注册的自定义选项处理器拿到的就是它 —— 处理器可以读选项、改算子。
type Cond struct {
	// Column 列名（原始值，未加引号；引号由方言在生成时加）。
	Column string
	// Op 算子名（已归一化：regex → like）。
	Op string
	// Omit 零值当成"没有值"。
	Omit bool
	// Path 字段在条件结构体里的索引路径。
	Path []int
	// Opts 标签里的全部选项（含未识别的 —— 读规则不认的选项忽略但保留）。
	Opts map[string]string
}

// group 是一个条件组。
type group struct {
	// comb 组内连接方式：and / or / not。
	comb string
	// items 组内的条件。
	items []*node
	// root 是否是顶层那层 —— 顶层隐式 AND，不加外层括号。
	root bool
}

// node 是条件树的一个节点：要么是叶子，要么是组。
type node struct {
	leaf  *leaf
	group *group
}

// leaf 是叶子条件：公开元信息 + 内部形态。
type leaf struct {
	*Cond
	// kind 字段形态（决定算子怎么取、怎么校验）。
	kind leafKind
}

// templateCache 按类型缓存条件树模板。
//
// 模板与值无关、跨方言复用（见 R6.5），因此可以一直缓存。
var templateCache sync.Map // reflect.Type -> *Template

// parseTemplate 解析一个条件结构体，得到条件树模板。
func parseTemplate(t reflect.Type) (*Template, error) {
	items, err := parseFields(t, nil)
	if err != nil {
		return nil, err
	}
	return &Template{
		root: &group{comb: "and", items: items, root: true},
		typ:  t,
	}, nil
}

// parseFields 解析一层结构体的字段，返回这一层的条件。
//
// path 是本层在顶层条件结构体里的索引路径前缀。
func parseFields(t reflect.Type, path []int) ([]*node, error) {
	base := util.DerefType(t)
	if base.Kind() != reflect.Struct {
		return nil, fmt.Errorf("query: %s 不是结构体，不能作为条件组", t)
	}
	var items []*node
	for i := 0; i < base.NumField(); i++ {
		sf := base.Field(i)
		if sf.PkgPath != "" { // 未导出字段不参与
			continue
		}
		tag := strings.TrimSpace(sf.Tag.Get("dbx"))
		if tag == "" || strings.HasPrefix(tag, "-") {
			// 未标注 dbx 或 dbx:"-" —— 直接忽略，不报错（见 3.1.6、3.1.7）。
			continue
		}
		fieldPath := append(append([]int{}, path...), i)
		if isGroupType(sf.Type) {
			n, ok, err := parseGroupField(base, sf, fieldPath)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			items = append(items, n)
			continue
		}
		n, err := parseLeaf(base, sf, fieldPath)
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return items, nil
}

// isGroupType 判断字段类型是不是「条件组」—— 即要展开成多列的结构体。
//
// 标量与内置单列类型（time.Time、sql.Null* 等）、实现了 driver.Valuer /
// sql.Scanner 的结构体，以及区间类型 Range[T]，都按单列处理（见 util.IsSingleColumn）。
func isGroupType(t reflect.Type) bool {
	return !util.IsSingleColumn(t)
}

// parseLeaf 解析一个叶子字段。
func parseLeaf(parent reflect.Type, sf reflect.StructField, path []int) (*node, error) {
	parts := util.SplitOpts(sf.Tag.Get("dbx"))
	if dup := util.DuplicateOpt(parts); dup != "" {
		if dup == util.OptCol {
			return nil, fmt.Errorf("query: %s.%s: 列名重复指定（首段与 col= 只能写一个）", parent.Name(), sf.Name)
		}
		return nil, fmt.Errorf("query: %s.%s: 选项 %q 重复", parent.Name(), sf.Name, dup)
	}
	opts := util.ParseOpts(parts)
	if err := leafComb(opts); err != nil {
		return nil, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	omit, err := boolOption(opts, optOmit)
	if err != nil {
		return nil, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	col := opts[util.OptCol]
	if col == "" {
		col = util.SnakeCase(sf.Name)
	}

	kind := classify(sf.Type)
	c := &Cond{
		Column: col,
		Omit:   omit,
		Path:   path,
		Opts:   opts,
	}
	// 算子：显式 op= 优先（regex 是 like 的别名）。
	if op, ok := opts[optOp]; ok {
		c.Op = normalizeOp(op)
	}
	// 业务注册过的选项先跑 —— 处理器可以改写算子。
	if err := applyCustomOptions(c); err != nil {
		return nil, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	// 没写算子就按字段类型推断。
	if c.Op == "" {
		c.Op = inferOp(kind)
	}
	// range 不走算子表 —— 它在 where.go 结构化展开为 gte + lt 的 and 组
	// （dbwm 不设 range 算子，见 prd-dbwm §4.1）。
	if c.Op != "range" {
		if _, ok := lookupOperator(c.Op); !ok {
			return nil, fmt.Errorf("query: %s.%s: 未知算子 %q（可用：%s）",
				parent.Name(), sf.Name, c.Op, strings.Join(operatorNames(), ", "))
		}
	}
	if err := validateOp(c.Op, kind, sf.Type); err != nil {
		return nil, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	return &node{leaf: &leaf{Cond: c, kind: kind}}, nil
}

// parseGroupField 解析一个条件组字段（写了 inline 的结构体字段）。
//
// 第二个返回值 false 表示"整个字段不参与"—— 没写 inline（或 inline=false）。
func parseGroupField(parent reflect.Type, sf reflect.StructField, path []int) (*node, bool, error) {
	parts := util.SplitOpts(sf.Tag.Get("dbx"))
	if dup := util.DuplicateOpt(parts); dup != "" {
		return nil, false, fmt.Errorf("query: %s.%s: 选项 %q 重复", parent.Name(), sf.Name, dup)
	}
	opts := util.ParseOpts(parts)
	// 组必须显式写 inline；没写（或 inline=false）整个字段忽略（等同 dbx:"-"）。
	inline, err := boolOption(opts, optInline)
	if err != nil {
		return nil, false, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	if !inline {
		return nil, false, nil
	}
	comb, err := groupComb(opts)
	if err != nil {
		return nil, false, fmt.Errorf("query: %s.%s: %w", parent.Name(), sf.Name, err)
	}
	if _, ok := opts[optOp]; ok {
		return nil, false, fmt.Errorf("query: %s.%s: op= 只能用在叶子字段上", parent.Name(), sf.Name)
	}
	if _, ok := opts[optOmit]; ok {
		return nil, false, fmt.Errorf("query: %s.%s: omit 只能用在叶子字段上（组是否参与由子条件决定）", parent.Name(), sf.Name)
	}
	items, err := parseFields(sf.Type, path)
	if err != nil {
		return nil, false, err
	}
	return &node{group: &group{comb: comb, items: items}}, true, nil
}

// normalizeOp 归一化算子名：转小写；regex 是 like 的别名（见 R4.5）。
func normalizeOp(op string) string {
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "regex" {
		return "like"
	}
	return op
}

// ---------------------------------------------------------------------------
// 自定义选项
// ---------------------------------------------------------------------------

// OptionHandler 处理一条读规则的自定义选项，把结果写进条件元信息。
type OptionHandler func(c *Cond, value string) error

var (
	optionMu      sync.RWMutex
	customOptions = map[string]OptionHandler{}
)

// RegisterOption 注册读规则作用域的业务自定义选项。
//
// 选项名不可覆盖：已注册的选项重名直接 panic（注册表纪律，见 rules/code.md §4.3）。
// 注册必须在该条件模型**第一次被解析**之前完成 —— 选项在解析期处理，
// 之后注册的选项对已缓存的模板无效。
//
//	query.RegisterOption("tenant", func(c *query.Cond, val string) error {
//		c.Opts["tenant"] = val
//		return nil
//	})
func RegisterOption(key string, h OptionHandler) {
	if key == "" {
		panic("query: 选项名不能为空")
	}
	if h == nil {
		panic("query: 选项处理器不能为 nil")
	}
	optionMu.Lock()
	defer optionMu.Unlock()
	if _, dup := customOptions[key]; dup {
		panic(fmt.Sprintf("query: 选项 %q 已注册，不可覆盖", key))
	}
	customOptions[key] = h
}

// lookupCustomOption 查找业务注册过的选项。
func lookupCustomOption(key string) (OptionHandler, bool) {
	optionMu.RLock()
	defer optionMu.RUnlock()
	h, ok := customOptions[key]
	return h, ok
}

// applyCustomOptions 把业务注册过的选项跑一遍。
//
// 选项按名字排序后逐个处理，保证处理顺序稳定。
func applyCustomOptions(c *Cond) error {
	var keys []string
	for k := range c.Opts {
		if _, ok := lookupCustomOption(k); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		h, _ := lookupCustomOption(k)
		if err := h(c, c.Opts[k]); err != nil {
			return err
		}
	}
	return nil
}
