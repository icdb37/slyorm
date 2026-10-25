package insert

import (
	"fmt"
	"reflect"

	"github.com/icdb37/slyorm/model"
	"github.com/icdb37/slyorm/util"
)

// ---------------------------------------------------------------------------
// 静态列 → 写侧列
//
// 列名、路径、inline 展开都在 model 包的遍历里完成；这里只做写规则解释：
//   - 选项取值校验（null / json / autopk 的布尔取值、deff 非空）；
//   - defv= 字面量按字段类型解析（解析期即校验）；
//   - 语义冲突的选项组合报错（deff+defv、json+null、autopk+null）；
//   - 跨规则选项（op=、comb=、读侧的 omit、建表侧的 type= …）忽略但保留。
// ---------------------------------------------------------------------------

// writeColumn 把 model 包的静态列解释成写侧列。
//
// parent 是模型类型（错误信息用），sc 是静态列元信息。
func writeColumn(parent reflect.Type, sc *model.Column) (Column, error) {
	c := Column{Column: sc}
	for k, v := range sc.Opts {
		if err := applyOption(&c, k, v); err != nil {
			return Column{}, fmt.Errorf("insert: %s.%s: %w", parent.Name(), sc.GoName, err)
		}
	}
	if err := validateColumn(&c); err != nil {
		return Column{}, fmt.Errorf("insert: %s.%s: %w", parent.Name(), sc.GoName, err)
	}
	return c, nil
}

// validateColumn 检查字段级选项之间是否互相冲突（见 prd-insert R11）。
func validateColumn(c *Column) error {
	if c.Has(keyDeff) && c.Has(keyDefv) {
		return fmt.Errorf("deff（默认值函数）与 defv（默认值）只能二选一")
	}
	if c.IsAutoPK() && c.BoolOpt(keyNull) {
		return fmt.Errorf("autopk 与 null 互斥：自增列零值交给数据库生成，不写 NULL")
	}
	if c.BoolOpt(keyJSON) && c.BoolOpt(keyNull) {
		return fmt.Errorf("json 与 null 互斥：json 列的值是序列化后的字符串，不适用零值写 NULL")
	}
	return nil
}

// applyOption 校验一条选项并解析需要预解析的值（defv 字面量）。
//
// 业务注册过的选项调 handler；其余不认的选项原样留在 Opts 里，不阻断业务
// （读规则专属的 op= / comb=、建表侧的 type= / default= / unique= …）。
func applyOption(c *Column, key string, val string) error {
	if h, ok := lookupCustomOption(key); ok {
		return h(c, val)
	}
	switch key {
	case keyNull, keyJSON, keyAutoPK:
		// 布尔选项：裸 flag 算 true，显式取值必须是合法布尔（null=false 表示关闭）。
		if _, ok := util.ParseBoolValue(val); !ok {
			return optErr(key, val)
		}
	case keyDeff:
		if val == "" {
			return fmt.Errorf("deff 需要指定默认值函数名")
		}
	case keyDefv:
		// 引号在选项解析时已去掉，这里直接按字段类型解析字面量。
		v, err := parseLiteral(val, c.Type)
		if err != nil {
			return fmt.Errorf("%w；默认值函数请改用 deff=", err)
		}
		c.DefaultValue = v
	}
	return nil
}
