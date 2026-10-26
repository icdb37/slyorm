package dbwm

import (
	"errors"
	"strings"
)

// ---------------------------------------------------------------------------
// 错误类型与类别
//
// 业务侧拿到数据库报错后要能判定类别（唯一冲突 / 类型不匹配 / 非空 /
// 外键 / CHECK / 其他），不必解析驱动错误文本 —— 类别由执行路径上的
// dbwm 识别（见 errclass.go），判定接口在 dbwm 与根包 slyorm 各给一份。
// ---------------------------------------------------------------------------

// Kind 是数据库错误的类别：业务侧据此判断该改数据、该改参数，还是按内部错误处理。
type Kind int

const (
	// KindUnknown 不是数据库错误（或认不出）—— 框架自身的错误落这里。
	KindUnknown Kind = iota
	// KindUnique 唯一索引 / 主键冲突。
	KindUnique
	// KindTypeMismatch 字段类型与值不匹配：值放不进列（或放不进读侧目标字段）。
	KindTypeMismatch
	// KindNotNull 非空约束不满足：列没给值且库里没有默认值。
	KindNotNull
	// KindForeignKey 外键约束不满足。
	KindForeignKey
	// KindCheck CHECK 约束不满足。
	KindCheck
	// KindOther 是数据库错误，但不属以上任何一类。
	KindOther
)

// String 返回类别的中文名（用于错误信息）。
func (k Kind) String() string {
	switch k {
	case KindUnique:
		return "唯一索引冲突"
	case KindTypeMismatch:
		return "字段类型与值不匹配"
	case KindNotNull:
		return "非空约束不满足"
	case KindForeignKey:
		return "外键约束不满足"
	case KindCheck:
		return "CHECK 约束不满足"
	case KindOther:
		return "数据库执行错误"
	}
	return "未知错误"
}

// 类别哨兵错误：调用方用 errors.Is 判定（ErrDatabase 是"是数据库错误"的哨兵，
// 任何已分类的数据库错误对它都为真）。
var (
	// ErrUnique 唯一索引 / 主键冲突。
	ErrUnique = errors.New("dbwm: 唯一索引冲突")
	// ErrTypeMismatch 字段类型与值不匹配。
	ErrTypeMismatch = errors.New("dbwm: 字段类型与值不匹配")
	// ErrNotNull 非空约束不满足。
	ErrNotNull = errors.New("dbwm: 非空约束不满足")
	// ErrForeignKey 外键约束不满足。
	ErrForeignKey = errors.New("dbwm: 外键约束不满足")
	// ErrCheck CHECK 约束不满足。
	ErrCheck = errors.New("dbwm: CHECK 约束不满足")
	// ErrDatabase 数据库执行错误（含上面各类）；不是数据库错误时不匹配。
	ErrDatabase = errors.New("dbwm: 数据库执行错误")
)

// Error 是数据库执行错误：带类别、动作、约束名与出错语句。
//
// 底层驱动错误原样保留在 Err 里 —— Unwrap 返回它，errors.As 能取到驱动
// 自己的错误类型，业务要细节时不必再解文本。
type Error struct {
	// Kind 错误类别。
	Kind Kind
	// Action 出错的动作，如"执行 INSERT 语句"。
	Action string
	// Constraint 报错的约束名或冲突列（驱动给得出才有）。
	Constraint string
	// Statement 出错的语句（已截断）。
	Statement string
	// Err 底层驱动错误。
	Err error
}

// Error 返回错误信息：动作、类别、约束名、驱动错误原文与语句。
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("dbwm: ")
	if e.Action != "" {
		b.WriteString(e.Action)
		b.WriteString("失败：")
	}
	b.WriteString(e.Kind.String())
	if e.Constraint != "" {
		b.WriteString("（约束 ")
		b.WriteString(e.Constraint)
		b.WriteString("）")
	}
	if e.Err != nil {
		b.WriteString("：")
		b.WriteString(e.Err.Error())
	}
	if e.Statement != "" {
		b.WriteString("；语句: ")
		b.WriteString(e.Statement)
	}
	return b.String()
}

// Unwrap 返回底层驱动错误。
func (e *Error) Unwrap() error { return e.Err }

// Is 让 errors.Is 按类别判定：类别哨兵与"是数据库错误"的哨兵都能命中。
func (e *Error) Is(target error) bool {
	if target == ErrDatabase {
		return e.Kind != KindUnknown
	}
	return sentinelOf(e.Kind) == target
}

// sentinelOf 返回类别对应的哨兵错误；没有对应哨兵的类别返回 nil。
func sentinelOf(k Kind) error {
	switch k {
	case KindUnique:
		return ErrUnique
	case KindTypeMismatch:
		return ErrTypeMismatch
	case KindNotNull:
		return ErrNotNull
	case KindForeignKey:
		return ErrForeignKey
	case KindCheck:
		return ErrCheck
	}
	return nil
}

// kindSentinels 是类别与哨兵的对照（判定顺序无关，各类互不重叠）。
var kindSentinels = []struct {
	kind Kind
	err  error
}{
	{KindUnique, ErrUnique},
	{KindTypeMismatch, ErrTypeMismatch},
	{KindNotNull, ErrNotNull},
	{KindForeignKey, ErrForeignKey},
	{KindCheck, ErrCheck},
}

// ---------------------------------------------------------------------------
// 错误构造
// ---------------------------------------------------------------------------

// execFail 构造执行失败的错误：识别驱动错误类别，带上动作、约束名与语句文本。
func (db *DB) execFail(action, stmt string, err error) error {
	return &Error{
		Kind:       classify(db.dialect, err),
		Action:     action,
		Constraint: constraintOf(err),
		Statement:  stmtForErr(stmt),
		Err:        err,
	}
}

// fail 构造不带语句文本的执行失败错误：事务、脚本等没有单条语句的场景。
func (db *DB) fail(action string, err error) error {
	return &Error{
		Kind:       classify(db.dialect, err),
		Action:     action,
		Constraint: constraintOf(err),
		Err:        err,
	}
}

// ---------------------------------------------------------------------------
// 判定接口
// ---------------------------------------------------------------------------

// KindOf 返回错误的数据库类别；不是数据库错误时是 KindUnknown。
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	// 没有 *Error 时按哨兵判定：只带哨兵的错误链也能给出类别。
	for _, ks := range kindSentinels {
		if errors.Is(err, ks.err) {
			return ks.kind
		}
	}
	return KindUnknown
}

// ConstraintOf 返回出错涉及的约束名 / 冲突列；认不出时返回空串。
func ConstraintOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Constraint
	}
	return ""
}

// IsUnique 判断错误是否为唯一索引 / 主键冲突。
func IsUnique(err error) bool { return errors.Is(err, ErrUnique) }

// IsTypeMismatch 判断错误是否为字段类型与值不匹配。
func IsTypeMismatch(err error) bool { return errors.Is(err, ErrTypeMismatch) }

// IsNotNull 判断错误是否为非空约束不满足。
func IsNotNull(err error) bool { return errors.Is(err, ErrNotNull) }

// IsForeignKey 判断错误是否为外键约束不满足。
func IsForeignKey(err error) bool { return errors.Is(err, ErrForeignKey) }

// IsCheck 判断错误是否为 CHECK 约束不满足。
func IsCheck(err error) bool { return errors.Is(err, ErrCheck) }
