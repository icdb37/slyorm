package util

import (
	"database/sql"
	"reflect"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 单列类型
// ---------------------------------------------------------------------------

// 内置单列类型不展开成多列；标量天然是一列；普通结构体要 inline 才展开。
func TestIsSingleColumn(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{name: "time.Time", typ: reflect.TypeOf(time.Time{}), want: true},
		{name: "sql.NullString", typ: reflect.TypeOf(sql.NullString{}), want: true},
		{name: "sql.NullInt64", typ: reflect.TypeOf(sql.NullInt64{}), want: true},
		{name: "普通结构体", typ: reflect.TypeOf(struct{ A string }{}), want: false},
		{name: "int", typ: reflect.TypeOf(0), want: true},
		{name: "string", typ: reflect.TypeOf(""), want: true},
		{name: "[]byte", typ: reflect.TypeOf([]byte(nil)), want: true},
		{name: "*int", typ: reflect.TypeOf(new(int)), want: true},
	}
	for _, tc := range cases {
		if got := IsSingleColumn(tc.typ); got != tc.want {
			t.Errorf("%s 的 IsSingleColumn = %v, 期望 %v", tc.name, got, tc.want)
		}
	}
}

// sql.Null* 家族的识别与取值。
func TestNullValue(t *testing.T) {
	rv := reflect.ValueOf(sql.NullString{String: "a", Valid: true})
	v, valid, ok := NullValue(rv)
	if !ok || !valid || v.(string) != "a" {
		t.Errorf("NullValue = %v(%v,%v), 期望 a(true,true)", v, valid, ok)
	}
	rv = reflect.ValueOf(sql.NullString{})
	_, valid, ok = NullValue(rv)
	if !ok || valid {
		t.Errorf("NullValue = %v(%v), 期望无效值", valid, ok)
	}
	// 不是该家族的结构体
	_, _, ok = NullValue(reflect.ValueOf(struct{ A string }{}))
	if ok {
		t.Error("普通结构体不该被当成 sql.Null* 家族")
	}
}

// ---------------------------------------------------------------------------
// 零值判定
// ---------------------------------------------------------------------------

// 零值判定：零值、nil 指针都是零值；非 nil 指针即使指向零值也不是零值。
func TestIsZero(t *testing.T) {
	var nilPtr *int
	i := 0
	cases := []struct {
		name string
		val  any
		want bool
	}{
		{name: "int 零值", val: 0, want: true},
		{name: "int 非零值", val: 1, want: false},
		{name: "空串", val: "", want: true},
		{name: "nil 指针", val: nilPtr, want: true},
		{name: "指向零值的指针", val: &i, want: false},
		{name: "零时刻", val: time.Time{}, want: true},
		{name: "有值的时刻", val: time.Now(), want: false},
	}
	for _, tc := range cases {
		if got := IsZero(reflect.ValueOf(tc.val)); got != tc.want {
			t.Errorf("%s 的 IsZero = %v, 期望 %v", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 区间
// ---------------------------------------------------------------------------

// 区间的边界由零值决定：一端是零值就当"没给"这一端。
func TestRangeBounds(t *testing.T) {
	r := Range[int]{Min: 3}
	if !r.HasMin() || r.HasMax() {
		t.Errorf("Range{Min:3} 的边界 = (%v,%v), 期望 (true,false)", r.HasMin(), r.HasMax())
	}
	if r.MinValue().(int) != 3 {
		t.Errorf("MinValue = %v, 期望 3", r.MinValue())
	}
	r = Range[int]{}
	if r.HasMin() || r.HasMax() {
		t.Error("零值区间两端都不算给了")
	}
	// 零时刻同样算"没给"
	tr := Range[time.Time]{}
	if tr.HasMin() || tr.HasMax() {
		t.Error("零时刻的两端都不算给了")
	}
}
