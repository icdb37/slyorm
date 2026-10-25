package insert

import (
	"fmt"
	"reflect"
)

// ---------------------------------------------------------------------------
// 入参归一与切片处理
// ---------------------------------------------------------------------------

// prepare 校验入参并解析**含表名**的写侧模型（本包独立执行路径用）。
func prepare(v any) (reflect.Value, *Model, error) {
	elem, _, err := prepareWrite(v)
	if err != nil {
		return reflect.Value{}, nil, err
	}
	full, err := ModelOf(elem.Type())
	if err != nil {
		return reflect.Value{}, nil, err
	}
	return elem, full, nil
}

// prepareWrite 校验入参并解析**不含表名**的写侧模型（IR 构造入口用，
// 投影可以是匿名结构体 —— 表名由调用方填，见 prd-slyorm R7.2）。
func prepareWrite(v any) (reflect.Value, *Model, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return reflect.Value{}, nil, fmt.Errorf("insert: 需要非空结构体指针")
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return reflect.Value{}, nil, fmt.Errorf("insert: 需要非空结构体指针")
	}
	m, err := writeModelOf(elem.Type())
	if err != nil {
		return reflect.Value{}, nil, err
	}
	return elem, m, nil
}

// sliceOf 把入参归一化成切片值：[]T 或 *[]T。
func sliceOf(vs any) (reflect.Value, error) {
	rv := reflect.ValueOf(vs)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("insert: InsertAll 需要非空切片指针")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice {
		return reflect.Value{}, fmt.Errorf("insert: InsertAll 需要切片，实际是 %s", rv.Kind())
	}
	return rv, nil
}

// structAt 取切片第 i 项，解引用成结构体值（元素可以是 T 或 *T）。
func structAt(rv reflect.Value, i int) (reflect.Value, error) {
	item := rv.Index(i)
	for item.Kind() == reflect.Pointer {
		if item.IsNil() {
			return reflect.Value{}, fmt.Errorf("insert: InsertAll 第 %d 项是 nil 指针", i)
		}
		item = item.Elem()
	}
	if item.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("insert: InsertAll 第 %d 项不是结构体", i)
	}
	return item, nil
}
