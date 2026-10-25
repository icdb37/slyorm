package rows

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"slyorm/model"
)

// ---------------------------------------------------------------------------
// 扫描目标构造
//
// 每列的落点按字段形态分四类（见 prd-slyorm R6.2）：
//   - 指针字段：传字段地址（**V），database/sql 原生处理 NULL → nil；
//   - time.Time / *time.Time：时间兼容目标（time.Time 直扫 / 字符串多布局 /
//     Unix 时间戳按数量级，一律 UTC）；
//   - json 标记字段：JSON 文本反序列化目标（NULL / 空串还原零值）；
//   - 其余基本值类型（含命名类型与 []byte）：NULL 兜底目标（NULL → Go 零值，
//     字符串源可宽松转数值 / 布尔）。
//
// 实现 sql.Scanner 的字段直扫（自定义类型自己负责形态）。
// ---------------------------------------------------------------------------

// timeType / timePtrType 时间字段的两种形态。
var (
	timeType    = reflect.TypeOf(time.Time{})
	timePtrType = reflect.TypeOf((*time.Time)(nil))
)

// scannerType 判定自定义扫描目标的接口。
var scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()

// scanAddrs 按计划的列序返回一行各列的扫描地址。
//
// 路径上的 nil 指针子结构预分配（路径保证指向可展开的子 struct）。
func (p *Plan) scanAddrs(elem reflect.Value) []any {
	out := make([]any, len(p.m.Columns))
	for i, c := range p.m.Columns {
		cur := reachField(elem, c)
		if c.IsJSON() {
			out[i] = flexJSON{addr: cur.Addr().Interface()}
			continue
		}
		if t := cur.Type(); t == timeType || t == timePtrType {
			out[i] = flexTime{addr: cur.Addr().Interface()}
			continue
		}
		if cur.Kind() == reflect.Pointer {
			out[i] = cur.Addr().Interface() // database/sql 原生 NULL → nil
			continue
		}
		if reflect.PointerTo(cur.Type()).Implements(scannerType) {
			out[i] = cur.Addr().Interface() // 自定义扫描目标
			continue
		}
		if isBasicValue(cur.Type()) {
			out[i] = nullZero{addr: cur.Addr().Interface()}
			continue
		}
		out[i] = cur.Addr().Interface() // 其余形态交给驱动
	}
	return out
}

// reachField 按索引路径抵达叶子字段；路径上的 nil 指针子结构预分配。
func reachField(elem reflect.Value, c *model.Column) reflect.Value {
	cur := elem
	last := len(c.Path) - 1
	for k, idx := range c.Path {
		cur = cur.Field(idx)
		if k < last && cur.Kind() == reflect.Pointer {
			if cur.IsNil() {
				cur.Set(reflect.New(cur.Type().Elem()))
			}
			cur = cur.Elem()
		}
	}
	return cur
}

// isBasicValue 是否为需要 NULL 兜底的基本值类型（含命名类型与 []byte 切片）。
func isBasicValue(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8
	}
	return false
}

// ---------------------------------------------------------------------------
// NULL 兜底目标
// ---------------------------------------------------------------------------

// nullZero 基本值类型字段的 NULL 兜底扫描目标：NULL 时写 Go 零值
//（string→""、数值→0、bool→false、[]byte→nil），非 NULL 按 driver.Value
// 契约转换；字符串源可宽松转数值 / 布尔（SQLite 动态类型下 TEXT 列读进
// 数值字段的场景），转不动报错。
type nullZero struct {
	addr any // *string / *int / *[]byte … 基本值类型字段地址
}

// Scan 实现 sql.Scanner。
func (z nullZero) Scan(src any) error {
	dv := reflect.ValueOf(z.addr).Elem()
	if src == nil {
		dv.SetZero()
		return nil
	}
	if s, ok := stringSrc(src); ok {
		return setFromString(dv, s)
	}
	switch s := src.(type) {
	case bool:
		if dv.Kind() == reflect.Bool {
			dv.SetBool(s)
			return nil
		}
	case int64:
		if dv.Kind() == reflect.Bool {
			// 与 stdlib driver.Bool 语义一致：仅 0/1 可转 bool。
			if s != 0 && s != 1 {
				return fmt.Errorf("rows: 无法把 %d 写入 bool", s)
			}
			dv.SetBool(s == 1)
			return nil
		}
		return setFromInt(dv, s)
	case float64:
		if isFloatKind(dv.Kind()) {
			dv.SetFloat(s)
			return nil
		}
		if isIntKind(dv.Kind()) || isUintKind(dv.Kind()) {
			return setFromInt(dv, int64(s))
		}
	case time.Time:
		if dv.Kind() == reflect.String {
			dv.SetString(s.Format(time.RFC3339Nano))
			return nil
		}
	}
	return fmt.Errorf("rows: 无法把 %T 写入 %s", src, dv.Type())
}

// stringSrc src 为 string / []byte 文本时取出字符串（驱动常把文本列按 []byte 返回）。
func stringSrc(src any) (string, bool) {
	switch v := src.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	}
	return "", false
}

// setFromString 把字符串源宽松写入目标（按目标类型解析）。
func setFromString(dv reflect.Value, s string) error {
	switch dv.Kind() {
	case reflect.String:
		dv.SetString(s)
	case reflect.Slice:
		if dv.Type().Elem().Kind() != reflect.Uint8 {
			break
		}
		dv.Set(reflect.ValueOf([]byte(s)).Convert(dv.Type()))
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		dv.SetBool(b)
	default:
		if isIntKind(dv.Kind()) {
			n, err := strconv.ParseInt(s, 10, dv.Type().Bits())
			if err != nil {
				return err
			}
			dv.SetInt(n)
			return nil
		}
		if isUintKind(dv.Kind()) {
			n, err := strconv.ParseUint(s, 10, dv.Type().Bits())
			if err != nil {
				return err
			}
			dv.SetUint(n)
			return nil
		}
		if isFloatKind(dv.Kind()) {
			f, err := strconv.ParseFloat(s, dv.Type().Bits())
			if err != nil {
				return err
			}
			dv.SetFloat(f)
			return nil
		}
		return fmt.Errorf("rows: 无法把字符串 %q 写入 %s", s, dv.Type())
	}
	return nil
}

// setFromInt 把整型源写入目标（含溢出校验）。
func setFromInt(dv reflect.Value, n int64) error {
	switch {
	case isIntKind(dv.Kind()):
		if dv.OverflowInt(n) {
			return fmt.Errorf("rows: %d 溢出 %s", n, dv.Type())
		}
		dv.SetInt(n)
	case isUintKind(dv.Kind()):
		if n < 0 || dv.OverflowUint(uint64(n)) {
			return fmt.Errorf("rows: %d 溢出 %s", n, dv.Type())
		}
		dv.SetUint(uint64(n))
	case isFloatKind(dv.Kind()):
		dv.SetFloat(float64(n))
	default:
		return fmt.Errorf("rows: 无法把 int64 写入 %s", dv.Type())
	}
	return nil
}

func isIntKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func isUintKind(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func isFloatKind(k reflect.Kind) bool {
	return k == reflect.Float32 || k == reflect.Float64
}

// ---------------------------------------------------------------------------
// 时间兼容目标
// ---------------------------------------------------------------------------

// flexTime 兼容三类驱动返回值的时间列扫描目标：
//   - time.Time（driver 已解析）；
//   - 日期时间字符串（"2006-01-02 15:04:05[.fff]"、ISO、日期、紧凑式）；
//   - Unix 时间戳数值（秒 / 毫秒 / 微秒 / 纳秒按数量级区分）——**按 UTC 解析**
//     （与写侧 defv 的 UTC 口径对称；模板 flexTime 的本地时区属差异，
//     见 prd-slyorm §8.12）。
//
// addr 为 *time.Time 或 **time.Time：NULL 时前者保持零值、后者置 nil。
type flexTime struct {
	addr any
}

// Scan 实现 sql.Scanner。
func (f flexTime) Scan(src any) error {
	t, ok, err := parseFlexTime(src)
	if err != nil {
		return err
	}
	switch a := f.addr.(type) {
	case *time.Time:
		if ok {
			*a = t
		}
	case **time.Time:
		if !ok {
			*a = nil
			return nil
		}
		if *a == nil {
			*a = new(time.Time)
		}
		**a = t
	default:
		return fmt.Errorf("rows: flexTime 不支持目标 %T", f.addr)
	}
	return nil
}

// parseFlexTime 把驱动返回值解析为时间；ok=false 表示 NULL。
func parseFlexTime(src any) (t time.Time, ok bool, err error) {
	switch v := src.(type) {
	case nil:
		return time.Time{}, false, nil
	case time.Time:
		return v, true, nil
	case []byte:
		return parseTimeString(string(v))
	case string:
		return parseTimeString(v)
	case int64:
		return unixToTime(v), true, nil
	case float64:
		return unixToTime(int64(v)), true, nil
	}
	return time.Time{}, false, fmt.Errorf("rows: 无法识别的时间源 %T", src)
}

// timeLayouts 覆盖常见 DATE / DATETIME / TIMESTAMP 字符串形态，
// 带时间的布局在前，避免日期布局抢先匹配掉时间部分。
var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04",
	"2006-01-02",
	"20060102150405",
}

// parseTimeString 解析时间字符串；空串按 NULL 处理。
func parseTimeString(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true, nil
		}
	}
	// 纯数字串按 Unix 时间戳兜底（部分驱动把数值列以文本返回）。
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return unixToTime(n), true, nil
	}
	return time.Time{}, false, fmt.Errorf("rows: 无法解析时间字符串 %q", s)
}

// unixToTime 按 Unix 时间戳数量级选择单位并按 UTC 解析：
// <1e11 秒、<1e14 毫秒、<1e17 微秒、其余纳秒。
func unixToTime(n int64) time.Time {
	switch {
	case n < 1e11:
		return time.Unix(n, 0).UTC()
	case n < 1e14:
		return time.UnixMilli(n).UTC()
	case n < 1e17:
		return time.UnixMicro(n).UTC()
	default:
		return time.Unix(0, n).UTC()
	}
}

// ---------------------------------------------------------------------------
// JSON 反序列化目标
// ---------------------------------------------------------------------------

// flexJSON json 标记列的扫描目标：TEXT 列的 JSON 文本 Unmarshal 进字段。
//
// addr 为 *T 或 **T：NULL / 空串时值目标还原零值、指针目标置 nil ——
// 与写侧"值字段恒序列化写入、nil 指针写 NULL"严格对称（prd-slyorm R6.2）。
type flexJSON struct {
	addr any // *map[string]any / *Extra / **T …
}

// Scan 实现 sql.Scanner。
func (f flexJSON) Scan(src any) error {
	dv := reflect.ValueOf(f.addr).Elem()
	if src == nil {
		dv.SetZero()
		return nil
	}
	s, ok := stringSrc(src)
	if !ok {
		return fmt.Errorf("rows: json 列不支持 %T 源", src)
	}
	if s == "" {
		dv.SetZero() // 空串与"未设置"同形：库里"空"只有一种表示
		return nil
	}
	if dv.Kind() == reflect.Pointer {
		if dv.IsNil() {
			dv.Set(reflect.New(dv.Type().Elem()))
		}
		dv = dv.Elem()
	}
	if err := json.Unmarshal([]byte(s), dv.Addr().Interface()); err != nil {
		return fmt.Errorf("rows: json 反序列化失败: %w", err)
	}
	return nil
}
