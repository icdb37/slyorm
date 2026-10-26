package dbwm

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 驱动错误识别
//
// 识别规则按驱动走：方言知道自己的驱动怎么报约束错误（SQLite 的错误码、
// MySQL 的 errno、PostgreSQL 的 SQLSTATE），dbwm 主体不认识具体驱动。
// 通用兜底按错误文本特征判定 —— 不依赖任何驱动的结构化信息。
// ---------------------------------------------------------------------------

// ErrorClassifier 是方言的可选扩展：识别驱动错误属于哪一类。
//
// Dialect 接口不含这个方法 —— 自定义方言不实现也能编译，只是识别精度落到
// 通用兜底（错误文本特征）。
type ErrorClassifier interface {
	// ClassifyError 判断 err 属于哪一类；ok 为 false 表示认不出。
	ClassifyError(err error) (Kind, bool)
}

// classify 识别驱动错误的类别：哨兵 → 方言 → 通用兜底。
//
// 都不中时归 KindOther（是数据库错误但类别不明），不是 KindUnknown ——
// KindUnknown 留给"不是数据库错误"（框架自身的错误）。
func classify(d Dialect, err error) Kind {
	if err == nil {
		return KindUnknown
	}
	for _, ks := range kindSentinels {
		if errors.Is(err, ks.err) {
			return ks.kind
		}
	}
	if c, ok := d.(ErrorClassifier); ok {
		if k, ok := c.ClassifyError(err); ok {
			return k
		}
	}
	return classifyGeneric(err)
}

// classifyGeneric 按错误文本特征判定类别（通用兜底）。
func classifyGeneric(err error) Kind {
	if k, ok := kindByMessage(err.Error()); ok {
		return k
	}
	return KindOther
}

// messagePatterns 是通用兜底的文本特征（不区分大小写），按顺序尝试。
var messagePatterns = []struct {
	kind Kind
	// any 命中任一特征即算该类别。
	any []string
}{
	{KindUnique, []string{
		"unique constraint",
		"duplicate entry",
		"duplicate key value",
	}},
	{KindNotNull, []string{
		"not null constraint",
		"not-null constraint",
		"cannot be null",
	}},
	{KindForeignKey, []string{
		"foreign key constraint",
	}},
	{KindCheck, []string{
		"check constraint",
	}},
	{KindTypeMismatch, []string{
		"datatype mismatch",
		"cannot store",
		"incorrect string value",
		"out of range value",
		"data too long",
		"string or blob too big",
		"value too long for type",
		"invalid input syntax for type",
		"invalid text representation",
		"string data right truncation",
	}},
}

// kindByMessage 按错误文本特征判定类别。
func kindByMessage(msg string) (Kind, bool) {
	low := strings.ToLower(msg)
	for _, p := range messagePatterns {
		for _, feat := range p.any {
			if strings.Contains(low, feat) {
				return p.kind, true
			}
		}
	}
	return KindOther, false
}

// ---------------------------------------------------------------------------
// SQLite
// ---------------------------------------------------------------------------

// SQLite 的主错误码（扩展码的主码也是 19）。
const (
	sqliteTooBig     = 18 // 值放不进列
	sqliteConstraint = 19 // 约束冲突（具体类型看消息）
	sqliteMismatch   = 20 // 类型不匹配
)

// classifySQLite 按 SQLite 的错误码与消息判定类别。
func classifySQLite(err error) (Kind, bool) {
	if code, ok := codeOf(err); ok {
		switch code {
		case sqliteMismatch, sqliteTooBig:
			return KindTypeMismatch, true
		case sqliteConstraint:
			// 约束族的具体类型看消息。
			return kindByMessage(err.Error())
		}
	}
	return kindByMessage(err.Error())
}

// ---------------------------------------------------------------------------
// MySQL
// ---------------------------------------------------------------------------

// mysqlErrnoRE 从 MySQL 错误文本里取 errno：Error 1062 (23000): Duplicate entry …
var mysqlErrnoRE = regexp.MustCompile(`^Error (\d+) \((\w{5})\):`)

// classifyMySQL 按 MySQL 的 errno 判定类别；取不到 errno 时按消息特征判定。
func classifyMySQL(err error) (Kind, bool) {
	if m := mysqlErrnoRE.FindStringSubmatch(err.Error()); m != nil {
		if no, cerr := strconv.Atoi(m[1]); cerr == nil {
			if k, ok := kindByMySQLErrno(no); ok {
				return k, true
			}
		}
	}
	return kindByMessage(err.Error())
}

// kindByMySQLErrno 按 MySQL 的 errno 判定类别。
func kindByMySQLErrno(no int) (Kind, bool) {
	switch no {
	case 1062, 1586, 1169: // ER_DUP_ENTRY / ER_DUP_ENTRY_WITH_KEY_NAME / ER_DUP_UNIQUE
		return KindUnique, true
	case 1048, 1364: // ER_BAD_NULL_ERROR / ER_NO_DEFAULT_FOR_FIELD
		return KindNotNull, true
	case 1451, 1452, 1216, 1217: // 外键约束相关
		return KindForeignKey, true
	case 3819, 4025: // ER_CHECK_CONSTRAINT_VIOLATED
		return KindCheck, true
	case 1366, 1264, 1406, 1292, 1690: // 截断 / 越界 / 过长 / 类型不符
		return KindTypeMismatch, true
	}
	return KindOther, false
}

// ---------------------------------------------------------------------------
// PostgreSQL
// ---------------------------------------------------------------------------

// classifyPostgres 按 SQLSTATE 判定类别；取不到 SQLSTATE 时按消息特征判定。
func classifyPostgres(err error) (Kind, bool) {
	if st, ok := sqlStateOf(err); ok {
		if k, ok := kindBySQLState(st); ok {
			return k, true
		}
	}
	return kindByMessage(err.Error())
}

// kindBySQLState 按 SQLSTATE 判定类别。
func kindBySQLState(st string) (Kind, bool) {
	switch st {
	case "23505": // unique_violation
		return KindUnique, true
	case "23502": // not_null_violation
		return KindNotNull, true
	case "23503": // foreign_key_violation
		return KindForeignKey, true
	case "23514": // check_violation
		return KindCheck, true
	case "22P02", "22001", "22007", "42804", "22032": // 类型 / 表示 / 越界类
		return KindTypeMismatch, true
	}
	return KindOther, false
}

// ---------------------------------------------------------------------------
// 驱动错误的结构化信息
// ---------------------------------------------------------------------------

// sqlStateOf 取驱动错误里的 SQLSTATE（PostgreSQL 的 pq / pgx 都提供）。
func sqlStateOf(err error) (string, bool) {
	var st interface{ SQLState() string }
	if errors.As(err, &st) {
		return st.SQLState(), true
	}
	return "", false
}

// codeOf 取驱动错误里的错误码（modernc 的 SQLite 错误提供 Code() int）。
func codeOf(err error) (int, bool) {
	var c interface{ Code() int }
	if errors.As(err, &c) {
		return c.Code(), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// 约束名提取
// ---------------------------------------------------------------------------

// constraintPatterns 是约束名的提取形态（按顺序尝试）。
var constraintPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)constraint failed:\s*([^()]+)`),     // SQLite：person.email
	regexp.MustCompile(`(?i)for key '([^']+)'`),                 // MySQL：person.uk_email
	regexp.MustCompile(`(?i)violates \w+ constraint "([^"]+)"`), // PostgreSQL：person_email_key
}

// constraintOf 从驱动错误里提取约束名 / 冲突列；认不出返回空串。
func constraintOf(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, re := range constraintPatterns {
		if m := re.FindStringSubmatch(msg); m != nil {
			return trimConstraintPrefix(m[1])
		}
	}
	return ""
}

// trimConstraintPrefix 去掉捕获文本里重复的 "constraint failed: " 前缀 ——
// modernc 的消息是 "constraint failed: UNIQUE constraint failed: person.email"。
func trimConstraintPrefix(s string) string {
	const p = "constraint failed: "
	low := strings.ToLower(s)
	if i := strings.LastIndex(low, p); i >= 0 {
		return strings.TrimSpace(s[i+len(p):])
	}
	return strings.TrimSpace(s)
}
