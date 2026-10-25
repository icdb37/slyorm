package insert

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 默认值
// ---------------------------------------------------------------------------

// Defaulter 生成字段的默认值。返回值会被写回结构体字段，
// 因此调用方在 Insert 之后可以直接读到最终值。
type Defaulter func(ctx context.Context) (any, error)

var defaultFuncs sync.Map // string -> Defaulter

// RegisterDefault 注册一个具名默认值函数，供 `deff=<名字>` 使用。
//
// 注册表纪律：函数名不可覆盖 —— 重复注册直接 panic（注册发生在装配期，
// 返回 error 必被调用方忽略，静默改写 "now" 的语义没法排查）。
func RegisterDefault(name string, fn Defaulter) {
	if name == "" {
		panic("insert: 默认值函数名不能为空")
	}
	if fn == nil {
		panic("insert: 默认值函数不能为 nil")
	}
	if _, loaded := defaultFuncs.LoadOrStore(name, fn); loaded {
		panic(fmt.Sprintf("insert: 默认值函数 %q 已注册，不可覆盖", name))
	}
}

// lookupDefault 查找已注册的默认值函数。
func lookupDefault(name string) (Defaulter, bool) {
	v, ok := defaultFuncs.Load(name)
	if !ok {
		return nil, false
	}
	return v.(Defaulter), true
}

// DefaultNames 返回已注册的默认值函数名。
func DefaultNames() []string {
	var names []string
	defaultFuncs.Range(func(k, _ any) bool {
		names = append(names, k.(string))
		return true
	})
	return names
}

func init() {
	// now 返回 UTC 当前时间，跨时区服务里比 time.Now() 更可预期。
	RegisterDefault("now", func(ctx context.Context) (any, error) {
		return time.Now().UTC(), nil
	})
}

// parseLiteral 把标签里的字面量解析成字段类型的值，供 `defv=<字面量>` 使用。
//
// 标签里的值本身是**字符串**（`=` 之后去掉首尾空白，再按 unquoteValue 处理引号），
// 按字段类型转换：
//
//	string   原样使用
//	bool     true/false/1/0/yes/no/on/off
//	int/uint 按十进制解析
//	float    按十进制解析
//	time.Time 见 parseTimeLiteral（RFC3339 / DateTime / DateOnly / Unix 时间戳）
//	[]byte   原样使用
//
// 未指定值（`defv` 或 `defv=`）时按空字符串处理：字符串 / []byte 直接用空值，
// 其它类型无法从空串转换，报错提示。
func parseLiteral(s string, t reflect.Type) (any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if s == "" {
		switch t.Kind() {
		case reflect.String:
			return "", nil
		case reflect.Slice:
			if t.Elem().Kind() == reflect.Uint8 {
				return []byte{}, nil
			}
		}
		return nil, fmt.Errorf("默认值不能为空")
	}
	if t == util.TimeType {
		v, err := parseTimeLiteral(s)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	switch t.Kind() {
	case reflect.String:
		return s, nil
	case reflect.Bool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "on":
			return true, nil
		case "0", "false", "no", "off":
			return false, nil
		}
		return nil, fmt.Errorf("默认值 %q 不是合法的布尔值", s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("默认值 %q 不是合法的整数", s)
		}
		return reflect.ValueOf(v).Convert(t).Interface(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("默认值 %q 不是合法的无符号整数", s)
		}
		return reflect.ValueOf(v).Convert(t).Interface(), nil
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("默认值 %q 不是合法的浮点数", s)
		}
		return reflect.ValueOf(v).Convert(t).Interface(), nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return []byte(s), nil
		}
	}
	return nil, fmt.Errorf("字段类型 %s 不支持字面量默认值", t)
}

// timeLayouts 是 time.Time 字面量接受的候选格式，按顺序尝试，命中第一条即为结果。
//
// 格式里没有时区信息的一律按 UTC 解析（time.Parse 的既定行为）—— 同一份模型元信息
// 在任何机器上都得到同一时刻，不会随本机时区漂移。
//
// 带小数秒的写法不需要单独列：time.Parse 在布局没写小数秒时也接受秒后面紧跟的小数部分，
// 因此 "2026-01-02 03:04:05.123" 与 "2026-01-02T03:04:05.123Z" 都能被下面的布局命中。
var timeLayouts = []string{
	time.RFC3339,                // 2026-01-02T03:04:05Z07:00
	"2006-01-02T15:04:05",       // 2026-01-02T03:04:05（无时区）
	time.DateTime,               // 2026-01-02 03:04:05（无时区）
	"2006-01-02 15:04:05Z07:00", // 2026-01-02 03:04:05+08:00
	"2006/01/02 15:04:05",       // 2026/01/02 03:04:05
	time.DateOnly,               // 2026-01-02（当天 00:00:00 UTC）
}

// parseTimeLiteral 解析 time.Time 字段的字面量默认值。
//
// 候选格式按 timeLayouts 依次尝试；纯数字按 Unix 时间戳解析（位宽决定单位）。
// 都命中不了就报错，并列出可用的写法。
func parseTimeLiteral(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if v, err := time.Parse(layout, s); err == nil {
			return v, nil
		}
	}
	if v, ok := parseUnixStamp(s); ok {
		return v, nil
	}
	return time.Time{}, fmt.Errorf("默认值 %q 不是合法的时间（支持 RFC3339、DateTime、DateOnly 或 Unix 时间戳）", s)
}

// parseUnixStamp 解析纯数字的 Unix 时间戳，位宽决定单位：
// 10 位秒、13 位毫秒、16 位微秒、19 位纳秒。
//
// 位宽必须精确命中标准宽度，避免把 "20260102" 这类紧凑日期当成秒级时间戳。
func parseUnixStamp(s string) (time.Time, bool) {
	switch len(s) {
	case 10, 13, 16, 19:
	default:
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	switch len(s) {
	case 10:
		return time.Unix(n, 0).UTC(), true
	case 13:
		return time.UnixMilli(n).UTC(), true
	case 16:
		return time.UnixMicro(n).UTC(), true
	default:
		return time.Unix(0, n).UTC(), true
	}
}
