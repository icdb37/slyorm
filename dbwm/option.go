package dbwm

// ---------------------------------------------------------------------------
// 构造选项
// ---------------------------------------------------------------------------

// Option 是构造选项：改句柄的构造期配置项（方言见 WithDialect）。
//
// 只在构造期生效，句柄建好后不再变；实现是函数、参数类型未导出，
// 包外只能用本包给的选项函数。
type Option func(*options)

// options 是构造期的可选项集合。
type options struct {
	dialect Dialect
}

// WithDialect 指定方言；不给这个选项时用标准 SQL 方言（ANSI）。
//
// 显式指定的方言优先于驱动名映射与连库探测（见 Open）。
func WithDialect(d Dialect) Option {
	return func(o *options) { o.dialect = d }
}

// applyOptions 跑一遍选项，返回生效的可选项集合。
func applyOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}
