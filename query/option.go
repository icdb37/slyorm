package query

import (
	"fmt"

	"github.com/icdb37/slyorm/util"
)

// ---------------------------------------------------------------------------
// 读规则的选项
//
// 读规则只认这几个选项；其余选项（写规则专属的、或者完全未知的）一律
// **忽略但保留** —— 保留在 Cond.Opts 里，供文档生成器、DDL 生成器读取。
// ---------------------------------------------------------------------------

const (
	// optOp 算子：op=<算子名>。
	optOp = "op"
	// optOmit 零值当成"没有值"（裸 flag 或显式布尔）。
	optOmit = "omit"
	// optComb 组内连接方式：comb=and|or|not。
	optComb = "comb"
	// optInline 结构体字段要展开成条件组必须显式写 inline。
	optInline = "inline"
)

// combAliases 是 comb= 的裸 flag 别名（只有这三个提供裸 flag 形式）。
var combAliases = []string{"and", "or", "not"}

// groupComb 取组字段的连接方式。
//
// comb= 的取值必须是 and/or/not；裸 flag（and/or/not）是它的别名。
// 两个都写 —— 报错（组合方式重复指定，猜不出调用方的意图）。
// 都没写 —— 默认 and。写了 comb= 但取值是空串或别的 —— 报错（取值必须是三者之一）。
func groupComb(opts map[string]string) (string, error) {
	var alias string
	for _, k := range combAliases {
		if _, ok := opts[k]; !ok {
			continue
		}
		if alias != "" {
			return "", fmt.Errorf("组合方式重复指定：%q 与 %q 只能写一个", alias, k)
		}
		alias = k
	}
	if v, ok := opts[optComb]; ok {
		if alias != "" {
			return "", fmt.Errorf("组合方式重复指定：comb=%s 与 %q 只能写一个", v, alias)
		}
		switch v {
		case "and", "or", "not":
			return v, nil
		}
		return "", fmt.Errorf("未知组合方式 comb=%s（可用：and、or、not）", v)
	}
	if alias != "" {
		return alias, nil
	}
	return "and", nil
}

// leafComb 检查叶子字段上有没有写组合方式 —— 有就报错（comb= 只能用在组字段上）。
func leafComb(opts map[string]string) error {
	if _, ok := opts[optComb]; ok {
		return fmt.Errorf("comb= 只能用在组字段上")
	}
	for _, k := range combAliases {
		if _, ok := opts[k]; ok {
			return fmt.Errorf("comb= 只能用在组字段上")
		}
	}
	return nil
}

// boolOption 读一个布尔选项的取值；取值不合法时由调用方报错。
//
// 选项不存在时返回 false（不算错）。
func boolOption(opts map[string]string, key string) (bool, error) {
	v, ok := opts[key]
	if !ok {
		return false, nil
	}
	b, valid := util.ParseBoolValue(v)
	if !valid {
		return false, fmt.Errorf("选项 %s 的取值 %q 不是合法布尔值（true/false/1/0/yes/no/on/off，裸 flag 等价 true）", key, v)
	}
	return b, nil
}
