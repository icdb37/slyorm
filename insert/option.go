package insert

import (
	"fmt"
	"sync"

	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 标签选项
// ---------------------------------------------------------------------------

const (
	// dbx 标签的选项名
	//
	// 本包只认这几个选项；`pk`（主键组）与 `auto`（普通自增列）不是写包的概念 ——
	// 主键组只在 UPDATE/DELETE 里用来定位，自增主键一律写 `autopk`。
	// 它们出现在标签里按未知选项处理：忽略但保留到 Column.Opts。
	keyCol    = util.OptCol
	keyNull   = "null"
	keyJSON   = "json"
	keyInline = "inline"
	keyAutoPK = "autopk"
	keyDeff   = "deff"
	keyDefv   = "defv"
)

// ---------------------------------------------------------------------------
// 自定义选项
// ---------------------------------------------------------------------------

// OptionHandler 处理一条自定义选项，把值写进列元信息。
type OptionHandler func(c *Column, value string) error

var (
	optionMu      sync.RWMutex
	customOptions = map[string]OptionHandler{}
)

// RegisterOption 注册业务自定义选项。
//
// 选项名不可覆盖：已注册的选项重名直接 panic（注册表纪律，见 rules/code.md §4.3）。
// 注意：模型元信息按类型缓存，选项只在模型**第一次被解析**时处理 ——
// 与 RegisterTable 一样，注册必须在该选项所在的模型第一次被使用之前完成。
//
//	insert.RegisterOption("encrypt", func(c *insert.Column, val string) error {
//		c.Opts["encrypt"] = val
//		return nil
//	})
func RegisterOption(key string, h OptionHandler) {
	if key == "" {
		panic("insert: 选项名不能为空")
	}
	if h == nil {
		panic("insert: 选项处理器不能为 nil")
	}
	optionMu.Lock()
	defer optionMu.Unlock()
	if _, dup := customOptions[key]; dup {
		panic(fmt.Sprintf("insert: 选项 %q 已注册，不可覆盖", key))
	}
	customOptions[key] = h
}

// lookupCustomOption 查找业务注册过的选项。
func lookupCustomOption(key string) (OptionHandler, bool) {
	optionMu.RLock()
	defer optionMu.RUnlock()
	h, ok := customOptions[key]
	return h, ok
}

// optErr 选项取值不合法时的统一报错。
func optErr(key, val string) error {
	return fmt.Errorf("选项 %s 的取值 %q 不是合法布尔值（true/false/1/0/yes/no/on/off，裸 flag 等价 true）", key, val)
}
