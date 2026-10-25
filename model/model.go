// Package model 承载表模型的静态元信息：列集合与主键标记。
//
// 它是**列名推导的唯一入口**（见 docs/prd-slyorm.md R2）：insert 的写清单、
// select（rows）的投影列集合都从同一份字段遍历派生，永不漂移。
// 产物只有**静态信息** —— 列名、抵达路径、字段类型、原始选项与主键标记；
// 值相关的写入规则（零值 / NULL / 默认值 / 自增列是否参与）由 insert 包
// 在这份元信息上解释，不进本包。
//
// 文件分工：
//
//	model.go   本文件 —— 模型与列的元信息、解析入口与按类型缓存
//	walk.go    字段遍历（inline 前缀链、json 单列、单列类型判定、列名冲突）
package model

import (
	"fmt"
	"reflect"
	"sync"
)

// 选项名：主键标记与自增主键（写规则的完整选项集见 insert / query 各自的 option.go）。
const (
	OptPK     = "pk"
	OptAutoPK = "autopk"
	OptJSON   = "json"
	OptInline = "inline"
)

// Column 一列的静态元信息。
//
// Opts 收着字段标签里的全部选项（键统一小写、值已去外层引号）：本包只解释
// pk / autopk / json / inline 四个，其余选项（null、deff=、op= …）原样保留，
// 由写包（insert）或读包（query）各自解释 —— 跨规则选项忽略但保留，不报错。
type Column struct {
	// GoName Go 字段名（错误信息用）。
	GoName string
	// Name 列名（inline 前缀已拼入；json 单列不带前缀）。
	Name string
	// Path 从根结构体抵达该字段的索引序列。
	Path []int
	// Type 字段的 Go 类型（未解指针）。
	Type reflect.Type
	// Opts 标签里的全部选项：选项名 → 选项值（裸 flag 值为空串）。
	Opts map[string]string
}

// Has 报告字段是否声明了某个选项。
func (c *Column) Has(key string) bool {
	_, ok := c.Opts[key]
	return ok
}

// Opt 取选项值；选项不存在时返回空串。
func (c *Column) Opt(key string) string {
	return c.Opts[key]
}

// BoolOpt 取布尔选项：裸 flag 算 true，显式取值按 true/false/1/0/… 解析，
// 取值不合法时保守取 false（这类错误在解析期已报出）。
func (c *Column) BoolOpt(key string) bool {
	return boolOpt(c.Opts, key)
}

// IsJSON 报告该列是否按 JSON 文本整列存取（写侧序列化、读侧反序列化）。
func (c *Column) IsJSON() bool { return c.BoolOpt(OptJSON) }

// IsPK 报告该列是否被裸 pk 标记为主键（非自增 / 复合主键）。
func (c *Column) IsPK() bool { return c.BoolOpt(OptPK) }

// IsAutoPK 报告该列是否为自增主键（autopk，隐含主键）。
func (c *Column) IsAutoPK() bool { return c.BoolOpt(OptAutoPK) }

// Model 一个 struct 类型的静态列集合。
//
// 元信息按**解指针后的类型**缓存（*User 与 User 是同一条），
// 构建成功后只读、并发安全。
type Model struct {
	// Type 解指针后的结构体类型。
	Type reflect.Type
	// Columns 列清单，顺序 = 字段声明序（inline 按字段位置展开）。
	Columns []*Column

	byName map[string]*Column
}

// Column 按列名取列元信息；不存在时第二个返回值为 false。
func (m *Model) Column(name string) (*Column, bool) {
	c, ok := m.byName[name]
	return c, ok
}

// HasColumn 报告列名是否在列集合里 —— 投影、过滤、排序的合法性都对着它校验。
func (m *Model) HasColumn(name string) bool {
	_, ok := m.byName[name]
	return ok
}

// Names 返回列名清单（声明序）。
func (m *Model) Names() []string {
	out := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		out[i] = c.Name
	}
	return out
}

// PKNames 返回主键列名（pk 标记列 + autopk 列，含复合主键，声明序）。
func (m *Model) PKNames() []string {
	var out []string
	for _, c := range m.Columns {
		if c.IsPK() || c.IsAutoPK() {
			out = append(out, c.Name)
		}
	}
	return out
}

// modelCache 按类型缓存模型元信息（解指针后的类型为键）。
var modelCache sync.Map // reflect.Type -> *Model

// Parse 解析一个模型值（指针或结构体值均可）的静态列集合，结果按类型缓存。
//
// 只看类型，与值无关。nil 指针也认 —— 解析不需要读字段值。
func Parse(v any) (*Model, error) {
	if v == nil {
		return nil, fmt.Errorf("model: 模型不能为 nil")
	}
	return TypeOf(reflect.TypeOf(v))
}

// TypeOf 解析一个结构体类型的静态列集合，结果按**解指针后的类型**缓存。
func TypeOf(t reflect.Type) (*Model, error) {
	base := derefType(t)
	if base.Kind() != reflect.Struct {
		return nil, fmt.Errorf("model: %s 不是结构体，无法解析 dbx 标签", t)
	}
	if v, ok := modelCache.Load(base); ok {
		return v.(*Model), nil
	}
	cols, err := walk(base)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("model: %s 未声明任何 dbx 字段", base)
	}
	m := &Model{Type: base, Columns: cols, byName: make(map[string]*Column, len(cols))}
	var autopk []string
	for _, c := range cols {
		m.byName[c.Name] = c
		if c.IsAutoPK() {
			autopk = append(autopk, c.GoName)
		}
	}
	if len(autopk) > 1 {
		return nil, fmt.Errorf("model: %s: autopk 只能有一个，发现 %v", base.Name(), autopk)
	}
	actual, _ := modelCache.LoadOrStore(base, m)
	return actual.(*Model), nil
}
