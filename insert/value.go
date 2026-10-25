package insert

import (
	"encoding/json"
	"fmt"
	"reflect"

	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 值与零值
// ---------------------------------------------------------------------------

// fieldAt 按索引路径取字段；路径上存在 nil 指针时返回该字段类型的零值。
func fieldAt(elem reflect.Value, c *Column) reflect.Value {
	rv, ok := util.ValueAt(elem, c.Path)
	if !ok {
		return reflect.Zero(c.Type)
	}
	return rv
}

// bindValue 取列的绑定值：
//
//   - json 列序列化成 JSON 字符串（nil 指针写 NULL）；
//   - 标了 null 的列，零值写 SQL NULL；
//   - nil 指针写 SQL NULL；
//   - 其余按原值绑定。
func bindValue(c *Column, rv reflect.Value) (any, error) {
	if c.BoolOpt(keyJSON) {
		// 只有 nil **指针**写 NULL；值字段的零值（nil map / 零值 struct）
		// 照常序列化写入（prd-insert R3 ⑨：{"city":"",...} / "null"）。
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			return nil, nil
		}
		b, err := json.Marshal(util.Value(rv))
		if err != nil {
			return nil, fmt.Errorf("insert: 字段 %s 序列化 JSON 失败: %w", c.GoName, err)
		}
		return string(b), nil
	}
	if c.BoolOpt(keyNull) && util.IsZero(rv) {
		return nil, nil // 零值写 NULL
	}
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil // nil 指针写 NULL
	}
	return util.Value(rv), nil
}

// setValue 把任意值写进字段，必要时做类型转换。
func setValue(dst reflect.Value, val any) error {
	if !dst.CanSet() {
		return fmt.Errorf("目标字段不可写（类型 %s）", dst.Type())
	}
	if val == nil {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}
	rv := reflect.ValueOf(val)
	switch {
	case rv.Type().AssignableTo(dst.Type()):
		dst.Set(rv)
		return nil
	case rv.Type().ConvertibleTo(dst.Type()):
		dst.Set(rv.Convert(dst.Type()))
		return nil
	case dst.Kind() == reflect.Pointer:
		el := reflect.New(dst.Type().Elem())
		switch {
		case rv.Type().AssignableTo(el.Elem().Type()):
			el.Elem().Set(rv)
		case rv.Type().ConvertibleTo(el.Elem().Type()):
			el.Elem().Set(rv.Convert(el.Elem().Type()))
		default:
			return fmt.Errorf("insert: 默认值类型 %s 不能赋给 %s", rv.Type(), dst.Type())
		}
		dst.Set(el)
		return nil
	default:
		return fmt.Errorf("insert: 默认值类型 %s 不能赋给 %s", rv.Type(), dst.Type())
	}
}

// setAutoPK 把自增值写进主键字段。
//
// 只在字段本来是零值时写：调用方显式给了值，就说明这一列的值是调用方指定的、
// 数据库没有生成自增值，回填等于覆盖调用方的意图（见 R6.6）。
// 判定用 IsZero，与 buildPlan 里"零值才交给数据库生成"的口径一致 ——
// 只有当初交给数据库生成的那一列，才会拿到自增值回填。
//
// 字段是指针（*int64）时：nil 就分配一个再写（与"零值不进 INSERT、由数据库生成"的
// 语义一致）；非 nil 视为调用方已经赋过值，不覆盖。
func setAutoPK(c *Column, elem reflect.Value, id int64) error {
	rv := fieldAt(elem, c)
	if !rv.CanSet() {
		return nil
	}
	if !util.IsZero(rv) {
		return nil // 已有显式值：不覆盖
	}
	if rv.Kind() == reflect.Pointer {
		// 走到这里指针必为 nil（IsZero 对指针只看 IsNil），分配一个再写。
		rv.Set(reflect.New(rv.Type().Elem()))
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv.SetInt(id)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		rv.SetUint(uint64(id))
	default:
		return fmt.Errorf("insert: 自增主键 %s 的类型 %s 不支持回填", c.GoName, rv.Type())
	}
	return nil
}
