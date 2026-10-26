package dbwm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// msgErr 是只有错误文本的驱动错误替身（MySQL 形态：errno 藏在文本里）。
type msgErr struct{ msg string }

func (e msgErr) Error() string { return e.msg }

// stateErr 是带 SQLSTATE 的驱动错误替身（PostgreSQL 形态）。
type stateErr struct {
	msg   string
	state string
}

func (e stateErr) Error() string    { return e.msg }
func (e stateErr) SQLState() string { return e.state }

// bareDialect 是不实现 ErrorClassifier 的方言：验证通用兜底。
type bareDialect struct{ Dialect }

// R2 唯一索引冲突：SQLite 重复主键 → 唯一冲突，约束名给出冲突列。
func TestClassifyUniqueViolation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Insert(ctx, &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "张三"}},
	}); err != nil {
		t.Fatalf("首次插入不该失败: %v", err)
	}
	_, err := db.Insert(ctx, &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "李四"}},
	})
	if err == nil {
		t.Fatal("主键重复该报错")
	}
	if !IsUnique(err) {
		t.Fatalf("错误 = %v，期望唯一索引冲突", err)
	}
	if got := KindOf(err); got != KindUnique {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnique)
	}
	if c := ConstraintOf(err); !strings.Contains(c, "person") {
		t.Fatalf("约束名 = %q，期望含 person", c)
	}
	if !errors.Is(err, ErrDatabase) {
		t.Fatalf("错误 = %v，期望是数据库错误", err)
	}
}

// R2 非空约束：往 NOT NULL 列写 NULL → 非空约束不满足。
func TestClassifyNotNullViolation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx,
		`CREATE TABLE nn (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	_, err := db.Insert(ctx, &Insert{
		Table:   "nn",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, nil}},
	})
	if err == nil {
		t.Fatal("往 NOT NULL 列写 NULL 该报错")
	}
	if !IsNotNull(err) {
		t.Fatalf("错误 = %v，期望非空约束不满足", err)
	}
	if got := KindOf(err); got != KindNotNull {
		t.Fatalf("类别 = %v，期望 %v", got, KindNotNull)
	}
}

// R2 类型与值不匹配：INTEGER 主键收到文本 → 类型不匹配。
func TestClassifyTypeMismatch(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Insert(context.Background(), &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{"abc", "张三"}},
	})
	if err == nil {
		t.Fatal("INTEGER 主键收到文本该报错")
	}
	if !IsTypeMismatch(err) {
		t.Fatalf("错误 = %v，期望类型与值不匹配", err)
	}
	if got := KindOf(err); got != KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, KindTypeMismatch)
	}
}

// R2 MySQL 形态：errno 藏在错误文本里 → 唯一冲突，约束名取到索引名。
func TestClassifyMySQLError(t *testing.T) {
	err := msgErr{msg: "Error 1062 (23000): Duplicate entry 'a@b' for key 'person.uk_email'"}
	if got := classify(MySQL, err); got != KindUnique {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnique)
	}
	if c := constraintOf(err); c != "person.uk_email" {
		t.Fatalf("约束名 = %q，期望 person.uk_email", c)
	}
	if got := classify(MySQL, msgErr{msg: "Error 1048 (23000): Column 'name' cannot be null"}); got != KindNotNull {
		t.Fatalf("类别 = %v，期望 %v", got, KindNotNull)
	}
	if got := classify(MySQL, msgErr{msg: "Error 1366 (22007): Incorrect string value: 'x' for column 'age'"}); got != KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, KindTypeMismatch)
	}
}

// R2 PostgreSQL 形态：SQLSTATE → 唯一冲突。
func TestClassifyPostgresError(t *testing.T) {
	err := stateErr{
		msg:   `pq: duplicate key value violates unique constraint "person_email_key"`,
		state: "23505",
	}
	if got := classify(PostgreSQL, err); got != KindUnique {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnique)
	}
	if c := constraintOf(err); c != "person_email_key" {
		t.Fatalf("约束名 = %q，期望 person_email_key", c)
	}
	if got := classify(PostgreSQL, stateErr{msg: "not null", state: "23502"}); got != KindNotNull {
		t.Fatalf("类别 = %v，期望 %v", got, KindNotNull)
	}
	if got := classify(PostgreSQL, stateErr{msg: "bad input", state: "22P02"}); got != KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, KindTypeMismatch)
	}
}

// R2 通用兜底：方言没实现 ErrorClassifier 时按错误文本特征判定。
func TestClassifyGenericFallback(t *testing.T) {
	d := bareDialect{Dialect: ANSI}
	cases := []struct {
		msg  string
		want Kind
	}{
		{"UNIQUE constraint failed: person.email", KindUnique},
		{"NOT NULL constraint failed: person.name", KindNotNull},
		{"FOREIGN KEY constraint failed", KindForeignKey},
		{"CHECK constraint failed: person.age", KindCheck},
		{"datatype mismatch", KindTypeMismatch},
		{"some driver failure", KindOther},
	}
	for _, c := range cases {
		if got := classify(d, msgErr{msg: c.msg}); got != c.want {
			t.Fatalf("错误 %q 的类别 = %v，期望 %v", c.msg, got, c.want)
		}
	}
}

// R3 认不出的驱动错误 → 其他数据库错误；框架自身的错误不是数据库错误。
func TestClassifyOtherAndFrameworkError(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Update(context.Background(), &Update{
		Table:   "person",
		Columns: []string{"age"},
		Values:  []any{1},
	})
	if err == nil {
		t.Fatal("没有 WHERE 该报错")
	}
	if got := KindOf(err); got != KindUnknown {
		t.Fatalf("框架错误的类别 = %v，期望 %v", got, KindUnknown)
	}
	if errors.Is(err, ErrDatabase) {
		t.Fatalf("框架错误不该是数据库错误: %v", err)
	}
	if got := classify(ANSI, msgErr{msg: "disk I/O error"}); got != KindOther {
		t.Fatalf("类别 = %v，期望 %v", got, KindOther)
	}
}

// R1 错误信息：类别中文名与出错语句都在里面，底层驱动错误保留。
func TestErrorMessageCarriesKindAndStatement(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Insert(ctx, &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "张三"}},
	}); err != nil {
		t.Fatalf("首次插入不该失败: %v", err)
	}
	_, err := db.Insert(ctx, &Insert{
		Table:   "person",
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "李四"}},
	})
	if err == nil {
		t.Fatal("主键重复该报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "唯一索引冲突") {
		t.Fatalf("错误信息 = %q，期望含类别中文名", msg)
	}
	if !strings.Contains(msg, `INSERT INTO "person"`) {
		t.Fatalf("错误信息 = %q，期望含语句文本", msg)
	}
	var dbErr *Error
	if !errors.As(err, &dbErr) {
		t.Fatal("错误应能取到 *dbwm.Error")
	}
	if dbErr.Err == nil {
		t.Fatal("底层驱动错误应保留")
	}
	if !strings.Contains(dbErr.Err.Error(), "UNIQUE") {
		t.Fatalf("底层错误 = %q，期望含驱动原文", dbErr.Err.Error())
	}
	if dbErr.Kind != KindUnique || dbErr.Action == "" {
		t.Fatalf("错误 = %+v，期望带类别与动作", dbErr)
	}
}

// R1 只带哨兵的错误链也能给出类别（读侧扫描错误走这条路）。
func TestKindOfSentinelOnlyChain(t *testing.T) {
	err := fmt.Errorf("rows: 无法把字符串 %q 写入 int: %w", "abc", ErrTypeMismatch)
	if got := KindOf(err); got != KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, KindTypeMismatch)
	}
	if !IsTypeMismatch(err) {
		t.Fatalf("错误 = %v，期望类型与值不匹配", err)
	}
}

// R6 查询未命中不是错误：ErrNoRows 是中间层信号，不属于任何类别。
func TestErrNoRowsIsNotAKind(t *testing.T) {
	db := newTestDB(t)
	_, err := collectOne(context.Background(), db, &Query{
		Table:   "person",
		Columns: []string{"id"},
		Where:   group(CombAnd, cond("name", OpEq, "查无此人")),
	}, personTargets("id"))
	if !errors.Is(err, ErrNoRows) {
		t.Fatalf("错误 = %v，期望是 ErrNoRows", err)
	}
	if got := KindOf(err); got != KindUnknown {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnknown)
	}
	if errors.Is(err, ErrDatabase) {
		t.Fatalf("查询未命中不该是数据库错误: %v", err)
	}
}

// R1 约束名提取：三种形态各取一次。
func TestConstraintExtraction(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"constraint failed: UNIQUE constraint failed: person.email (19)", "person.email"},
		{"UNIQUE constraint failed: person.email", "person.email"},
		{"Error 1062 (23000): Duplicate entry 'a' for key 'person.uk_email'", "person.uk_email"},
		{`pq: duplicate key value violates unique constraint "person_email_key"`, "person_email_key"},
	}
	for _, c := range cases {
		if got := constraintOf(msgErr{msg: c.msg}); got != c.want {
			t.Fatalf("错误 %q 的约束名 = %q，期望 %q", c.msg, got, c.want)
		}
	}
	if got := constraintOf(msgErr{msg: "disk I/O error"}); got != "" {
		t.Fatalf("约束名 = %q，期望空串", got)
	}
}
