package util

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"time"
)

// ---------------------------------------------------------------------------
// 单列类型
// ---------------------------------------------------------------------------

// TimeType 是 time.Time 的类型，零值判定要特殊处理。
var TimeType = reflect.TypeOf(time.Time{})

var (
	// valuerType / scannerType 用于识别「单列类型」与「嵌套结构」。
	valuerType  = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()

	// intervalType 是区间类型的接口，Range[T] 实现它（见 range.go）。
	intervalType = reflect.TypeOf((*Interval)(nil)).Elem()
)

// IsSingleColumn 判断类型是否按「整体一列」处理 —— 不展开成多列。
//
// 标量类型（整型、字符串、切片等）天然是一列；
// 结构体里按单列处理的类型：
//   - time.Time；
//   - sql.Null* 家族（判据见 IsNullType）；
//   - 实现了 driver.Valuer 或 sql.Scanner 的类型；
//   - 区间类型 Range[T]（它是一个条件，不是一组列）。
//
// 其余结构体在写规则里是「嵌套结构」，要显式 inline 才展开；
// 在读规则里是「条件组」，同样要显式 inline 才解析（见 query 包）。
func IsSingleColumn(t reflect.Type) bool {
	t = DerefType(t)
	if t.Kind() != reflect.Struct {
		return true
	}
	if t == TimeType {
		return true
	}
	if IsNullType(t) {
		return true
	}
	if t.Implements(valuerType) || reflect.PointerTo(t).Implements(valuerType) {
		return true
	}
	if t.Implements(scannerType) || reflect.PointerTo(t).Implements(scannerType) {
		return true
	}
	if t.Implements(intervalType) || reflect.PointerTo(t).Implements(intervalType) {
		return true
	}
	return false
}

// DerefType 解引用指针类型，返回底层类型；非指针类型原样返回。
func DerefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// IsNullType 判断类型是否属于 sql.Null* 家族。
//
// 判据：结构体恰好两个字段，其中一个名为 Valid 且是 bool ——
// sql.NullString 等是 {值, Valid}，sql.Null[T] 是 {V, Valid}。
func IsNullType(t reflect.Type) bool {
	_, _, ok := nullFields(t)
	return ok
}

// nullFields 返回 sql.Null* 家族里「值字段」与「Valid 字段」的下标。
func nullFields(t reflect.Type) (valueIdx, validIdx int, ok bool) {
	t = DerefType(t)
	if t.Kind() != reflect.Struct || t.NumField() != 2 {
		return 0, 0, false
	}
	validIdx = -1
	for i := 0; i < 2; i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // 未导出字段取不到
			return 0, 0, false
		}
		if f.Name == "Valid" && f.Type.Kind() == reflect.Bool {
			validIdx = i
		}
	}
	if validIdx < 0 {
		return 0, 0, false
	}
	return 1 - validIdx, validIdx, true
}

// NullValueType 返回 sql.Null* 家族携带的值的类型；不是该家族时返回 nil。
func NullValueType(t reflect.Type) reflect.Type {
	valueIdx, _, ok := nullFields(t)
	if !ok {
		return nil
	}
	return DerefType(t).Field(valueIdx).Type
}

// NullValue 取出 sql.Null* 家族的值。
//
// 第二个返回值是 Valid（是否有效）；第三个返回值 false 表示该值不是
// sql.Null* 家族 —— 调用方按普通结构体处理。
func NullValue(rv reflect.Value) (any, bool, bool) {
	rv = DerefValue(rv)
	if !rv.IsValid() {
		return nil, false, false
	}
	valueIdx, validIdx, ok := nullFields(rv.Type())
	if !ok {
		return nil, false, false
	}
	return rv.Field(valueIdx).Interface(), rv.Field(validIdx).Bool(), true
}

// ---------------------------------------------------------------------------
// 值与零值
// ---------------------------------------------------------------------------

// IsZero 判断字段是否为零值。
//
// time.Time 特殊处理：数据库里的零时间（0001-01-01 00:00:00 UTC）读回来之后，
// wall/ext/loc 三个私有字段不再全零，reflect 的结构体零值判定会失灵，
// 因此改用 time.Time.IsZero()——它按"时刻"判断，零时刻就是零值。
//
// 指针的语义保持不变：nil 才是零值，非 nil 指针即使指向零值也算"显式赋过值"。
func IsZero(rv reflect.Value) bool {
	if !rv.IsValid() {
		return true
	}
	switch rv.Kind() {
	case reflect.Struct:
		if rv.Type() == TimeType {
			if !rv.CanInterface() {
				return rv.IsZero()
			}
			t, _ := rv.Interface().(time.Time)
			return t.IsZero()
		}
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Field(i)
			if !f.CanInterface() {
				if !f.IsZero() {
					return false
				}
				continue
			}
			if !IsZero(f) {
				return false
			}
		}
		return true
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	case reflect.Slice, reflect.Map:
		return rv.Len() == 0
	}
	return rv.IsZero()
}

// IsNil 判断值是否为 nil（指针/切片/映射/接口）。
func IsNil(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// DerefValue 解引用指针值；遇到 nil 指针返回无效值（reflect.Value{}）。
func DerefValue(rv reflect.Value) reflect.Value {
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	return rv
}

// Value 取字段的绑定值：指针会解引用（time.Time 除外，交给驱动处理）。
func Value(rv reflect.Value) any {
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		el := rv.Elem()
		if el.Kind() != reflect.Struct || el.Type() == TimeType {
			return el.Interface()
		}
	}
	return rv.Interface()
}

// ValueAt 按索引路径取值；路径中遇到 nil 指针时返回 false。
func ValueAt(elem reflect.Value, path []int) (reflect.Value, bool) {
	v := elem
	for _, x := range path {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		v = v.Field(x)
	}
	return v, true
}
