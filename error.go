package slyorm

import (
	"github.com/icdb37/slyorm/dbwm"
)

// ---------------------------------------------------------------------------
// 错误判定
//
// 业务侧拿到数据库报错后要能判定类别 —— 唯一冲突提示"已存在"、类型与值
// 不匹配判参数错误、其余按内部错误处理 —— 不必解析驱动错误文本。
// 类别定义与识别规则在 dbwm（见 docs/prd-error.md），这里给业务侧判定接口。
// ---------------------------------------------------------------------------

// Kind 是数据库错误的类别（dbwm.Kind 的别名）。
type Kind = dbwm.Kind

// 类别常量（dbwm 的同名常量，业务侧不必 import 中间层）。
const (
	// KindUnknown 不是数据库错误（或认不出）—— 框架自身的错误落这里。
	KindUnknown = dbwm.KindUnknown
	// KindUnique 唯一索引 / 主键冲突。
	KindUnique = dbwm.KindUnique
	// KindTypeMismatch 字段类型与值不匹配：值放不进列（或放不进读侧目标字段）。
	KindTypeMismatch = dbwm.KindTypeMismatch
	// KindNotNull 非空约束不满足。
	KindNotNull = dbwm.KindNotNull
	// KindForeignKey 外键约束不满足。
	KindForeignKey = dbwm.KindForeignKey
	// KindCheck CHECK 约束不满足。
	KindCheck = dbwm.KindCheck
	// KindOther 是数据库错误，但不属以上任何一类。
	KindOther = dbwm.KindOther
)

// ErrorKind 返回错误的数据库类别；不是数据库错误时是 KindUnknown。
func ErrorKind(err error) Kind { return dbwm.KindOf(err) }

// ConstraintOf 返回出错涉及的约束名 / 冲突列；认不出时返回空串。
func ConstraintOf(err error) string { return dbwm.ConstraintOf(err) }

// IsUnique 判断错误是否为唯一索引 / 主键冲突。
func IsUnique(err error) bool { return dbwm.IsUnique(err) }

// IsTypeMismatch 判断错误是否为字段类型与值不匹配。
func IsTypeMismatch(err error) bool { return dbwm.IsTypeMismatch(err) }

// IsNotNull 判断错误是否为非空约束不满足。
func IsNotNull(err error) bool { return dbwm.IsNotNull(err) }

// IsForeignKey 判断错误是否为外键约束不满足。
func IsForeignKey(err error) bool { return dbwm.IsForeignKey(err) }

// IsCheck 判断错误是否为 CHECK 约束不满足。
func IsCheck(err error) bool { return dbwm.IsCheck(err) }
