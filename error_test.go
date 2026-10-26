package slyorm

import (
	"strings"
	"testing"
)

// R4 业务侧判定：唯一索引冲突 → IsUnique / ErrorKind / ConstraintOf。
func TestBusinessUniqueConflict(t *testing.T) {
	_, users, base := openUsers(t)
	if _, err := base.ExecContext(tctx,
		`CREATE UNIQUE INDEX uk_users_name ON users(name)`); err != nil {
		t.Fatalf("建唯一索引失败: %v", err)
	}
	if err := users.Create(tctx, &user{Name: "张三"}); err != nil {
		t.Fatalf("首次创建不该失败: %v", err)
	}
	err := users.Create(tctx, &user{Name: "张三"})
	if err == nil {
		t.Fatal("重名创建该报错")
	}
	if !IsUnique(err) {
		t.Fatalf("错误 = %v，期望唯一索引冲突", err)
	}
	if got := ErrorKind(err); got != KindUnique {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnique)
	}
	if c := ConstraintOf(err); !strings.Contains(c, "name") {
		t.Fatalf("约束名 = %q，期望含 name", c)
	}
}

// R4 框架自身的错误不是数据库错误：类别未知，判定接口都不命中。
func TestBusinessFrameworkErrorIsNotDatabaseError(t *testing.T) {
	_, users, _ := openUsers(t)
	// 条件为空 → 拒绝全表更新（框架规则，不是数据库报错）。
	err := users.Update(tctx, &userCond{}, &user{Name: "张三"})
	if err == nil {
		t.Fatal("空条件更新该报错")
	}
	if got := ErrorKind(err); got != KindUnknown {
		t.Fatalf("类别 = %v，期望 %v", got, KindUnknown)
	}
	if IsUnique(err) || IsTypeMismatch(err) || IsNotNull(err) ||
		IsForeignKey(err) || IsCheck(err) {
		t.Fatalf("错误 = %v，期望不命中任何类别", err)
	}
}

// R6 查询未命中不是错误：门面给空结果，业务侧不进错误分支。
func TestBusinessNoRowsIsNotError(t *testing.T) {
	_, users, _ := openUsers(t)
	miss, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: 99})
	if err != nil {
		t.Fatalf("未命中不该报错: %v", err)
	}
	if miss != nil {
		t.Fatalf("未命中应返回 nil，得到 %+v", miss)
	}
	list, err := users.Query[userCond, user](tctx, &userCond{ID: 99})
	if err != nil {
		t.Fatalf("未命中不该报错: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("未命中应返回空列表，得到 %d 行", len(list))
	}
}

// R5 读侧值放不进目标字段 → 类型与值不匹配。
func TestBusinessTypeMismatchOnScan(t *testing.T) {
	_, users, base := openUsers(t)
	// 库里塞一条 status 为文本的行：SQLite 动态类型收下了，读进 int 字段时才发现。
	if _, err := base.ExecContext(tctx, `INSERT INTO users
		(name, status, tags, created_at, created_by, updated_at, updated_by)
		VALUES ('张三', 'abc', '', '0001-01-01 00:00:00', 0, '0001-01-01 00:00:00', 0)`); err != nil {
		t.Fatalf("插行失败: %v", err)
	}
	rows, err := users.Query[userCond, user](tctx, &userCond{})
	if err == nil {
		t.Fatalf("文本读进 int 字段该报错，实际读到 %d 行", len(rows))
	}
	if !IsTypeMismatch(err) {
		t.Fatalf("错误 = %v，期望类型与值不匹配", err)
	}
	if got := ErrorKind(err); got != KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, KindTypeMismatch)
	}
	// 错误信息里能看到源值与目标类型（R5.2）。
	if msg := err.Error(); !strings.Contains(msg, `"abc"`) || !strings.Contains(msg, "解析进 int") {
		t.Fatalf("错误信息 = %q，期望含源值与目标类型", msg)
	}
}
