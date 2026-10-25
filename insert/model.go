package insert

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"slyorm/model"
	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 写侧模型
//
// 静态列集合（列名、路径、选项）来自 model 包 —— 列名推导的唯一入口；
// 本包在它之上解释写规则：选项校验、defv 字面量解析。
//
// 表名推导（TableName()/RegisterTable/类型名）只服务**本包独立使用**的
// 执行路径（Insert / InsertAll / BuildInsert）；IR 构造入口
//（BuildInsertIR / BuildInsertAllIR / BuildUpdateIR）不解析表名，IR 的
// Table 留空由调用方填 —— 门面（Collection）填自己绑定的表名，投影是
// 匿名结构体也不受影响（见 prd-slyorm R1.4 / R7.2）。
// ---------------------------------------------------------------------------

// Column 描述一个参与写入的列：静态元信息 + 写规则解释结果。
//
// 字段标签里的选项统一放在 Opts 里（继承自 model.Column）：
//
//	c.Opts["null"]           // 声明了 null 选项（裸 flag，值是空串）
//	c.Opts["deff"] == "now"  // deff=now
//
// 与写入无关的选项（pk、读规则的 op=、建表侧的 type= …）原样留在 Opts 里，
// 不阻断写入 —— 由解释它的包各取所需。
type Column struct {
	*model.Column
	// DefaultValue defv= 解析出来的字面量默认值（按字段类型解析，解析期校验）。
	// 有没有字面量默认值看 Opts 里有没有 "defv"。
	DefaultValue any
}

// IsAutoPK 报告该列是否为自增主键（autopk）。
func (c *Column) IsAutoPK() bool { return c.Column.IsAutoPK() }

// Model 描述一个写入模型。
//
// 经 ValidateModel / IR 构造入口拿到的模型**不含表名**（Schema / Table 为空）；
// 只有本包独立执行的路径（Insert 等）经 ModelOf 拿到带表名的模型。
type Model struct {
	// Schema 表前缀（PostgreSQL 的 schema、SQLite 的 attached database 名），默认空。
	Schema string
	// Table 表名。
	Table string
	// Columns 参与写入的列，顺序 = 字段声明序（inline 按字段位置展开）。
	Columns []Column
	// autoPK 自增主键列（最多一个；nil 表示没有）。
	autoPK *Column
}

// TableRef 返回带前缀的表引用，例如 "public.person"。
func (m *Model) TableRef() string {
	if m.Schema == "" {
		return m.Table
	}
	return m.Schema + "." + m.Table
}

// AutoPKColumn 返回自增主键列名；没有时为空串。
func (m *Model) AutoPKColumn() string {
	if m.autoPK == nil {
		return ""
	}
	return m.autoPK.Name
}

// modelCache 按类型缓存**不含表名**的写侧模型（解指针后的类型为键，
// 与 model 包同口径）。表名不走缓存：推导很便宜，且 RegisterTable
// 在首次使用之后调用也能生效。
var modelCache sync.Map // reflect.Type -> *Model

// writeModelOf 取（或构建）不含表名的写侧模型，结果按类型缓存；
// 构建失败不缓存（同一个错误每次调用都会再报，见 prd-slyorm R13.3）。
func writeModelOf(t reflect.Type) (*Model, error) {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if v, ok := modelCache.Load(base); ok {
		return v.(*Model), nil
	}
	static, err := model.TypeOf(base)
	if err != nil {
		return nil, err
	}
	m := &Model{}
	for _, sc := range static.Columns {
		c, err := writeColumn(base, sc)
		if err != nil {
			return nil, err
		}
		if c.IsAutoPK() {
			col := c
			m.autoPK = &col
		}
		m.Columns = append(m.Columns, c)
	}
	actual, _ := modelCache.LoadOrStore(base, m)
	return actual.(*Model), nil
}

// ValidateModel 校验一个类型的写规则（选项取值、语义冲突、defv 字面量解析），
// 不解析表名 —— 供门面在构造期把 M 的标签错误提前暴露（见 prd-slyorm R1.5）。
func ValidateModel(t reflect.Type) error {
	_, err := writeModelOf(t)
	return err
}

// ModelOf 解析写入模型（含表名），供本包独立使用的执行路径。
//
// 匿名结构体没法推导表名，在这里报错；表名按
// TableName() 方法 > RegisterTable 登记 > 类型名 snake_case 推导。
// 返回的模型是缓存的写侧模型加表名的浅拷贝（列切片只读共享）。
func ModelOf(t reflect.Type) (*Model, error) {
	m, err := writeModelOf(t)
	if err != nil {
		return nil, err
	}
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if base.Name() == "" {
		return nil, fmt.Errorf("insert: 匿名结构体没有表名：请实现 TableName() 方法，或用 RegisterTable 登记")
	}
	schema, table, err := resolveTableName(base)
	if err != nil {
		return nil, err
	}
	return &Model{Schema: schema, Table: table, Columns: m.Columns, autoPK: m.autoPK}, nil
}

// ---------------------------------------------------------------------------
// 表名推导（写包独立使用时的机制；Collection 显式传表名，不用这套）
// ---------------------------------------------------------------------------

// TableNamer 允许模型用方法指定表名，优先级最高。
type TableNamer interface {
	TableName() string
}

// tableRegistry 保存显式登记的表名。
var tableRegistry sync.Map // reflect.Type -> string

// resolveTableName 确定表名。
//
// 优先级：TableName() 方法 > RegisterTable 登记 > 类型名 snake_case。
// 返回值可以是 "public.person" 这样的带前缀形式。
func resolveTableName(t reflect.Type) (schema, table string, err error) {
	name := util.SnakeCase(t.Name())
	if registered, ok := lookupTable(t); ok {
		name = registered
	}
	if tn, ok := reflect.New(t).Interface().(TableNamer); ok {
		if n := tn.TableName(); n != "" {
			name = n
		}
	}
	return splitTableName(name)
}

// RegisterTable 显式登记结构体对应的表名，
// 优先级低于 TableName() 方法，高于类型名推导。
//
// 注意：模型元信息按类型缓存，因此登记要在该类型**第一次被使用之前**完成。
func RegisterTable[T any](name string) {
	tableRegistry.Store(reflect.TypeOf((*T)(nil)).Elem(), name)
}

// lookupTable 查找登记的表名。
func lookupTable(t reflect.Type) (string, bool) {
	v, ok := tableRegistry.Load(t)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// splitTableName 拆出表前缀与表名，前缀默认为空。
func splitTableName(name string) (schema, table string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", fmt.Errorf("insert: 表名不能为空")
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		schema, table = name[:i], name[i+1:]
	} else {
		table = name
	}
	return schema, table, nil
}
