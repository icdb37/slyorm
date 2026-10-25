package model

import (
	"fmt"
	"reflect"
	"strings"

	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 字段遍历 —— 列名推导的唯一入口
//
// 规则（见 prd-insert R2 / R3，读写两侧共用）：
//   - 未导出字段、未标注 dbx 的字段、dbx:"-" 的字段不参与；
//   - 嵌套结构（非单列类型的结构体）必须显式 inline 才展开，
//     第一段是列名前缀（末尾下划线可写可不写），前缀沿嵌套链逐级累加；
//   - json 标记的字段按单列处理，不带前缀；
//   - 列名 = col= > 首段 > 字段名 snake_case；同一模型内列名不得重复；
//   - 嵌套结构上只认 col 与 inline，其它选项解析期报错（单列语义写在子字段上）。
// ---------------------------------------------------------------------------

// walk 遍历一个结构体类型的全部 dbx 叶子字段，产出列清单（声明序）。
func walk(t reflect.Type) ([]*Column, error) {
	return walkFields(t, "", nil, map[string]bool{})
}

// field 是正在解析的一个字段：宿主结构体、字段本身、标签选项与抵达路径。
type field struct {
	parent reflect.Type
	sf     reflect.StructField
	opts   map[string]string
	path   []int
}

// walkFields 解析一层结构体；prefix 是本层列名公共前缀，basePath 是索引路径前缀，
// seen 是整个模型已出现的列名（跨层查重）。
func walkFields(t reflect.Type, prefix string, basePath []int, seen map[string]bool) ([]*Column, error) {
	t = derefType(t)
	var cols []*Column
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" { // 未导出字段不解析
			continue
		}
		tag := strings.TrimSpace(sf.Tag.Get("dbx"))
		if tag == "" || strings.HasPrefix(tag, "-") { // 未标注 dbx 或 dbx:"-" 不解析
			continue
		}
		opts, err := parseTag(t, sf, tag)
		if err != nil {
			return nil, err
		}
		f := field{parent: t, sf: sf, opts: opts, path: append(append([]int{}, basePath...), i)}
		if !util.IsSingleColumn(sf.Type) && !boolOpt(opts, OptJSON) {
			children, err := walkNested(f, prefix, seen)
			if err != nil {
				return nil, err
			}
			cols = append(cols, children...)
			continue
		}
		c, err := walkLeaf(f, prefix)
		if err != nil {
			return nil, err
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("model: %s 的列名 %q 重复", t.Name(), c.Name)
		}
		seen[c.Name] = true
		cols = append(cols, c)
	}
	return cols, nil
}

// parseTag 解析字段的 dbx 标签：切分、查重复、转成选项表。
func parseTag(parent reflect.Type, sf reflect.StructField, tag string) (map[string]string, error) {
	parts := util.SplitOpts(tag)
	if dup := util.DuplicateOpt(parts); dup != "" {
		if dup == util.OptCol {
			return nil, fmt.Errorf("model: %s.%s: 列名重复指定（首段与 col= 只能写一个）", parent.Name(), sf.Name)
		}
		return nil, fmt.Errorf("model: %s.%s: 选项 %q 重复", parent.Name(), sf.Name, dup)
	}
	return util.ParseOpts(parts), nil
}

// walkNested 解析嵌套结构字段（必须显式 inline，否则整组忽略）。
func walkNested(f field, parentPrefix string, seen map[string]bool) ([]*Column, error) {
	opts := f.opts
	if _, ok := validBool(opts, OptInline); !ok {
		return nil, fmt.Errorf("model: %s.%s: 选项 inline 的取值 %q 不是合法布尔值",
			f.parent.Name(), f.sf.Name, opts[OptInline])
	}
	if !boolOpt(opts, OptInline) {
		return nil, nil // 没写 inline（或 inline=false）：整组忽略，等同 dbx:"-"
	}
	for k := range opts {
		if k != util.OptCol && k != OptInline {
			return nil, fmt.Errorf("model: %s.%s: 嵌套结构只支持 inline，不认选项 %q（单列语义请写在子字段上）",
				f.parent.Name(), f.sf.Name, k)
		}
	}
	prefix := parentPrefix + normalizePrefix(opts[util.OptCol])
	return walkFields(f.sf.Type, prefix, f.path, seen)
}

// walkLeaf 解析叶子字段。
func walkLeaf(f field, prefix string) (*Column, error) {
	opts := f.opts
	name := opts[util.OptCol]
	if name == "" {
		name = util.SnakeCase(f.sf.Name)
	}
	// 本包解释的四个选项取值不合法要在解析期报出；其余选项留给 insert / query 解释。
	for _, key := range []string{OptJSON, OptPK, OptAutoPK, OptInline} {
		if _, ok := opts[key]; ok {
			if _, valid := validBool(opts, key); !valid {
				return nil, fmt.Errorf("model: %s.%s: 选项 %s 的取值 %q 不是合法布尔值",
					f.parent.Name(), f.sf.Name, key, opts[key])
			}
		}
	}
	// json 与 inline 互斥（prd-insert R3 ⑨/R11.5）：json 表示整列存取，inline 表示拆列，
	// 同时写等于没说 —— 在列名推导的唯一入口拦下，读写两侧都不再各自判一遍。
	if boolOpt(opts, OptJSON) && boolOpt(opts, OptInline) {
		return nil, fmt.Errorf("model: %s.%s: json 与 inline 互斥（json 是整列存取，inline 是拆成多列），只能写一个",
			f.parent.Name(), f.sf.Name)
	}
	// 走到叶子分支还带 inline，说明字段不是结构体 —— inline 只能用在结构体字段上。
	if boolOpt(opts, OptInline) {
		return nil, fmt.Errorf("model: %s.%s: inline 只能用在结构体字段上", f.parent.Name(), f.sf.Name)
	}
	c := &Column{
		GoName: f.sf.Name,
		Name:   name,
		Path:   f.path,
		Type:   f.sf.Type,
		Opts:   opts,
	}
	if !c.IsJSON() {
		c.Name = prefix + name // json 单列不带前缀
	}
	return c, nil
}

// normalizePrefix 规整列名前缀：末尾没有下划线就补一个，空前缀保持为空。
func normalizePrefix(s string) string {
	if s == "" || strings.HasSuffix(s, "_") {
		return s
	}
	return s + "_"
}

// validBool 校验布尔选项的取值并返回解析结果。
func validBool(opts map[string]string, key string) (bool, bool) {
	v, ok := opts[key]
	if !ok {
		return false, true
	}
	return util.ParseBoolValue(v)
}

// boolOpt 与 Column.BoolOpt 同口径，供本包内部使用。
func boolOpt(opts map[string]string, key string) bool {
	b, _ := validBool(opts, key)
	return b
}

// derefType 解引用指针类型；非指针原样返回。
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
