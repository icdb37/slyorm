package util

import (
	"strings"
	"unicode"
)

// OptCol 是选项表里"列名"那一项的键名。
//
// 标签的第一段是列名（写规则下嵌套结构里是列名前缀），统一记在这个键下。
const OptCol = "col"

// SplitOpts 按逗号切分标签值，括号与引号内的逗号不参与切分。
//
// 例如 `type=decimal(10,2),null` 会切成 ["type=decimal(10,2)", "null"]。
//
// 引号只有**成对出现**时才是分组标记；落单的引号按普通字符处理，
// 这样 `defv=it's,null` 仍能正确切成两段。
//
// 切分出的每一段都会先去掉两端的空白字符。
func SplitOpts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
					i++ // 成对引号表示一个引号
					continue
				}
				quote = 0
			}
		case c == '\'' || c == '"':
			if hasClosingQuote(s, i+1, c) {
				quote = c
			}
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

// ParseOpts 把切分结果解析成选项表：选项名 → 选项值。
//
// 选项名统一转小写（dbx:"id,NULL" 与 dbx:"id,null" 是一回事）；
// 选项值**按给定处理** —— 不做大小写转换，只把**外层引号**去掉（见 UnquoteValue）。
//
// 裸 flag 的值为空串，等价于 true。
//
// **第一段是列名**（读规则下是列名，写规则下嵌套结构里是列名前缀），
// 统一记在 OptCol 下。
func ParseOpts(items []string) map[string]string {
	out := make(map[string]string, len(items))
	for i, it := range items {
		if it == "" {
			continue
		}
		var key = it
		var val = ""
		if k, v, ok := strings.Cut(it, "="); ok {
			key = strings.ToLower(strings.TrimSpace(k))
			if key == "" {
				continue
			}
			val = strings.TrimSpace(v)
		} else {
			key = strings.ToLower(strings.TrimSpace(key))
		}
		if i == 0 { // 第一段为列名
			key = OptCol
			if val == "" {
				val = it // 列名本身（没有 = 时，名字就在这一段里）
			}
		}
		val = UnquoteValue(val)
		out[key] = val // 以最后操作符为准
	}
	return out
}

// DuplicateOpt 找出重复出现的选项名；没有重复返回空串。
//
// 重复选项视为笔误，由调用方在解析期报错。
// 第一段按列名算（与 ParseOpts 一致），因此 `dbx:"id,col=other"` 会被报成
// 列名重复指定。
func DuplicateOpt(items []string) string {
	seen := map[string]bool{}
	for i, it := range items {
		if it == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(it))
		if k, _, ok := strings.Cut(it, "="); ok {
			key = strings.ToLower(strings.TrimSpace(k))
		}
		if i == 0 {
			key = OptCol
		}
		if seen[key] {
			return key
		}
		seen[key] = true
	}
	return ""
}

// UnquoteValue 去掉取值外层的引号。
//
// 引号是分组标记（切分时引号内的逗号不切分），所以取值要把它脱掉：
//
//	'hello world'   → hello world
//	'it''s'         → it's        （成对的两个引号表示一个引号）
//	"abc"           → abc
//	it's            → it's        （没有外层引号，内容原样保留）
//	pre'quoted'post → pre'quoted'post
//	'abc            → 'abc        （落单的引号按普通字符处理，只有**成对出现**才是分组标记）
func UnquoteValue(s string) string {
	if len(s) >= 2 {
		q := s[0]
		if (q == '\'' || q == '"') && s[len(s)-1] == q {
			inner := s[1 : len(s)-1]
			return strings.ReplaceAll(inner, string(q)+string(q), string(q))
		}
	}
	return s
}

// ParseBoolValue 解析布尔选项的取值。
//
// 裸 flag（空串）算 true；显式取值接受 true/false/1/0/yes/no/on/off（大小写不敏感）。
// 第二个返回值 false 表示取值写错了 —— 由调用方在解析期报错。
func ParseBoolValue(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return true, true
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	return false, false
}

// BoolOpt 判断选项表里的布尔选项是否为真。
//
// 取值不合法时保守取 false（这类错误在解析模型时就报出来了）。
func BoolOpt(opts map[string]string, key string) bool {
	v, ok := opts[key]
	if !ok {
		return false
	}
	b, ok := ParseBoolValue(v)
	return ok && b
}

// SnakeCase 把 Go 字段名转成列名：CreatedAt -> created_at，ID -> id。
func SnakeCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == ' ':
			b.WriteRune('_')
			continue
		case unicode.IsUpper(r):
			if i > 0 {
				prev := runes[i-1]
				var next rune
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				// 小写/数字 -> 大写，或大写 -> 大写+小写（HTTPStatus）时分词
				if unicode.IsLower(prev) || unicode.IsDigit(prev) ||
					(unicode.IsUpper(prev) && unicode.IsLower(next)) {
					b.WriteRune('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasClosingQuote 判断 s[i:] 之后有没有与 q 相同的引号。
func hasClosingQuote(s string, i int, q byte) bool {
	for j := i; j < len(s); j++ {
		if s[j] == q {
			return true
		}
	}
	return false
}
