package slyorm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/icdb37/slyorm/util"

	_ "modernc.org/sqlite"
)

var tctx = context.Background()

// 装配期注入"当前操作用户"：审计列 deff=curuser 的取值来源（R12）。
// 全局注册表只许注册一次，测试里用一个固定用户。
const auditUID = int64(7)

func init() {
	RegisterUser(func(context.Context) (any, error) { return auditUID, nil })
}

// 表模型：自增主键 + 唯一业务列 + 审计结构 + json 列。
type AuditInfo struct {
	CreatedAt time.Time `dbx:"created_at,deff=now"`
	CreatedBy int64     `dbx:"created_by,deff=curuser"`
	UpdatedAt time.Time `dbx:"updated_at,deff=now"`
	UpdatedBy int64     `dbx:"updated_by,deff=curuser"`
}

type user struct {
	ID        int64    `dbx:"id,autopk"`
	Name      string   `dbx:"name"`
	Status    int      `dbx:"status"`
	Tags      []string `dbx:"tags,json"`
	Remark    *string  `dbx:"remark"`
	AuditInfo `dbx:",inline"`
}

// 局部更新投影（不含主键）。
type userRename struct {
	Name      string `dbx:"name"`
	AuditInfo `dbx:",inline"`
}

// 查询条件（omit：零值当没有值 —— 全零时解析不出任何条件）。
type userCond struct {
	ID     int64   `dbx:"id,omit"`
	Name   string  `dbx:"name,op=like,omit"`
	Status int     `dbx:"status,omit"`
	Tags   []int64 `dbx:"id"` // 复用 id 列做集合查询；空切片 = 无条件
}

// omitCond 单字段 omit：零值时整体为空条件。
type omitCond struct {
	ID int64 `dbx:"id,omit"`
}

const userDDL = `CREATE TABLE users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	status INT NOT NULL DEFAULT 0,
	tags TEXT NOT NULL DEFAULT '',
	remark TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT '0001-01-01 00:00:00',
	created_by INT NOT NULL DEFAULT 0,
	updated_at TIMESTAMP NOT NULL DEFAULT '0001-01-01 00:00:00',
	updated_by INT NOT NULL DEFAULT 0
)`

// openUsers 开内存库、手写建表、构造访问器（表名显式传，与模型类型解耦）。
func openUsers(t *testing.T) (*DB, *Collection, *sql.DB) {
	t.Helper()
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	base.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = base.Close() })
	if _, err := base.ExecContext(tctx, userDDL); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	db := NewSQL(base)
	users, err := NewCollection[user](db, "users")
	if err != nil {
		t.Fatalf("NewCollection: %v", err)
	}
	return db, users, base
}

// R1 构造：表名空/非法报错；标签错误（含 json+inline 冲突、写规则冲突）在构造期报错。
type jsonInline struct {
	A string `dbx:"a,json,inline"` // json 与 inline 互斥（prd-insert R11.5，model 层拦截）
}
type writeRuleBad struct {
	Name string `dbx:"name,deff=now,defv=x"` // deff+defv 冲突（构造期过写规则校验，R1.5）
}

func TestNewCollectionErrors(t *testing.T) {
	db := NewSQL(nil)
	if _, err := NewCollection[user](db, ""); err == nil {
		t.Fatal("空表名应当报错")
	}
	if _, err := NewCollection[user](db, "bad name"); err == nil {
		t.Fatal("含空白的表名应当报错")
	}
	if _, err := NewCollection[jsonInline](db, "t"); err == nil {
		t.Fatal("json+inline 冲突应当在构造期报错（model 层拦截）")
	}
	if _, err := NewCollection[writeRuleBad](db, "t"); err == nil {
		t.Fatal("写规则冲突（deff+defv）应当在构造期报错，不留到首次写入（R1.5）")
	}
}

// R2.4 读侧投影列不在 M 的列集合 → 报错；同一个错误连续两次调用都报（不缓存错误）。
type leakProj struct {
	ID     int64  `dbx:"id"`
	Secret string `dbx:"secret"` // M 从未声明的列
}

func TestReadProjectionColumnCheck(t *testing.T) {
	_, users, _ := openUsers(t)
	mustCreate(t, users, &user{Name: "张三"})
	for i := 0; i < 2; i++ {
		if _, err := users.Get[IDQuery, leakProj](tctx, &IDQuery{ID: 1}); err == nil {
			t.Fatalf("第 %d 次调用：读侧投影列越出表模型应当报错", i+1)
		}
	}
	if _, err := users.Query[userCond, leakProj](tctx, &userCond{}); err == nil {
		t.Fatal("Query 的投影列校验应当报错")
	}
	if _, err := users.List[userCond, leakProj](tctx, nil); err == nil {
		t.Fatal("List 的投影列校验应当报错")
	}
}

// 投影类型是指针 → 在门面层报错并指出写法（读侧与写侧）。
func TestPointerProjection(t *testing.T) {
	_, users, _ := openUsers(t)
	if _, err := users.Get[IDQuery, *user](tctx, &IDQuery{ID: 1}); err == nil {
		t.Fatal("指针型投影（读侧）应当报错")
	}
	if err := users.Create(tctx, &user{Name: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := users.Create[*user](tctx, new(*user)); err == nil {
		t.Fatal("指针型投影（写侧）应当报错")
	}
}

// R7.2/F5 匿名结构体投影插入：门面路径不依赖 insert 的表名机制，匿名投影可用。
func TestAnonymousProjectionCreate(t *testing.T) {
	_, users, _ := openUsers(t)
	p := &struct {
		Name string `dbx:"name"`
	}{Name: "匿名"}
	if err := users.Create(tctx, p); err != nil {
		t.Fatalf("匿名结构体投影插入: %v", err)
	}
	got, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: 1})
	if err != nil || got == nil || got.Name != "匿名" {
		t.Fatalf("读回 = (%v, %v)", got, err)
	}
}

// R11 slyorm.Range 与 util.Range 是同一类型（类型别名，可互换）。
func TestRangeAlias(t *testing.T) {
	var r slyormRange = utilRange{Min: 1, Max: 2}
	_ = r
}

// 供 TestRangeAlias 使用的两个具名形态（同一底层类型）。
type slyormRange = Range[int]
type utilRange = util.Range[int]

// R3 Get：未命中 (nil, nil)；多行取首行；空条件报错（零值标量是生效条件）。
func TestGet(t *testing.T) {
	_, users, _ := openUsers(t)
	mustCreate(t, users, &user{Name: "张三"})

	got, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: 1})
	if err != nil || got == nil || got.Name != "张三" {
		t.Fatalf("Get = (%v, %v)", got, err)
	}
	miss, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: 99})
	if err != nil || miss != nil {
		t.Fatalf("未命中应返回 (nil, nil)，得到 (%v, %v)", miss, err)
	}

	// Xget 动态分派：不用写 Q。
	x, err := users.Xget[user](tctx, &IDQuery{ID: 1})
	if err != nil || x == nil {
		t.Fatalf("Xget = (%v, %v)", x, err)
	}
	// 不带 dbx 标签的 q 解析不出条件 → 报错（模板静默查全表是坑）。
	if _, err := users.Xget[user](tctx, struct{ Name string }{Name: "x"}); err == nil {
		t.Fatal("空条件应当在 Get 报错")
	}
	// 零值标量是生效条件（id = 0）：不报"缺少条件"，返回未命中。
	if miss2, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: 0}); err != nil || miss2 != nil {
		t.Fatalf("id=0 是生效条件，期望 (nil, nil)，得到 (%v, %v)", miss2, err)
	}
}

// R4 Query：空条件查全表；条件过滤；过滤列不存在报错。
type noColCond struct {
	Bad string `dbx:"no_such_col,omit"`
}

func TestQuery(t *testing.T) {
	_, users, _ := openUsers(t)
	mustCreate(t, users, &user{Name: "a"})
	mustCreate(t, users, &user{Name: "b"})

	all, err := users.Query[userCond, user](tctx, &userCond{})
	if err != nil || len(all) != 2 {
		t.Fatalf("空条件应查全表: %v %v", all, err)
	}
	some, err := users.Query[userCond, userRename](tctx, &userCond{Name: "a"})
	if err != nil || len(some) != 1 || some[0].Name != "a" {
		t.Fatalf("like 过滤 = %+v %v", some, err)
	}
	if _, err := users.Query[noColCond, user](tctx, &noColCond{Bad: "x"}); err == nil {
		t.Fatal("条件列不存在应当报错")
	}
}

// R5 List：分页 + 排序 + 总数；排序白名单；Size=0 取全量。
func TestList(t *testing.T) {
	_, users, _ := openUsers(t)
	for _, n := range []string{"a", "b", "c"} {
		mustCreate(t, users, &user{Name: n})
	}

	page, err := users.List[userCond, user](tctx, &ListRequest[userCond]{
		PageQuery: PageQuery{Page: 2, Size: 1, Sort: []string{"-id"}},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Total != 3 || len(page.Data) != 1 || page.Data[0].Name != "b" {
		t.Fatalf("total=%d data=%v", page.Total, page.Data)
	}

	// Size ≤0：不分页取全量（页码被忽略，不触发 dbwm 的 Size==0&&Page>1 报错）。
	full, err := users.List[userCond, user](tctx, &ListRequest[userCond]{
		PageQuery: PageQuery{Page: 5, Size: 0},
	})
	if err != nil || full.Total != 3 || len(full.Data) != 3 {
		t.Fatalf("Size=0 应取全量: total=%d n=%d err=%v", full.Total, len(full.Data), err)
	}

	// 排序列不存在 → 报错。
	if _, err := users.List[userCond, user](tctx, &ListRequest[userCond]{
		PageQuery: PageQuery{Sort: []string{"-no_such"}},
	}); err == nil {
		t.Fatal("排序列不存在应当报错")
	}
}

// R7 Create：自增回填、审计列填充（deff=now / curuser）、json 序列化、nil 指针 NULL。
func TestCreate(t *testing.T) {
	_, users, base := openUsers(t)
	u := &user{Name: "张三", Tags: []string{"x"}}
	if err := users.Create(tctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID != 1 {
		t.Fatalf("自增回填失败: ID=%d", u.ID)
	}
	if u.CreatedBy != auditUID || u.UpdatedBy != auditUID {
		t.Fatalf("审计列未按 curuser 填充: %d/%d", u.CreatedBy, u.UpdatedBy)
	}
	if u.CreatedAt.IsZero() {
		t.Fatal("created_at 未由 deff=now 填充")
	}

	var tags string
	var remark any
	var createdBy int64
	err := base.QueryRowContext(tctx,
		`SELECT tags, remark, created_by FROM users WHERE id=?`, u.ID).
		Scan(&tags, &remark, &createdBy)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if tags != `["x"]` || remark != nil || createdBy != auditUID {
		t.Fatalf("落库形态 tags=%q remark=%v created_by=%d", tags, remark, createdBy)
	}
}

// R7.4 Creates：同一张表、自增按行序回填；R11 投影列不在集合报错。
type alien struct {
	Bad string `dbx:"no_such_col"`
}

func TestCreates(t *testing.T) {
	_, users, _ := openUsers(t)
	ps := []user{{Name: "a"}, {Name: "b"}}
	if err := users.Creates(tctx, ps); err != nil {
		t.Fatalf("Creates: %v", err)
	}
	if ps[0].ID != 1 || ps[1].ID != 2 {
		t.Fatalf("自增按行序回填失败: %d %d", ps[0].ID, ps[1].ID)
	}
	if err := users.Create(tctx, &alien{}); err == nil {
		t.Fatal("投影列不在表列集合应当报错")
	}
}

// R8 Update：局部更新（投影全列进 SET）、主键剔除、审计刷新、空条件报错。
func TestUpdate(t *testing.T) {
	_, users, _ := openUsers(t)
	u := &user{Name: "旧名"}
	mustCreate(t, users, u)

	if err := users.Update[IDQuery, userRename](tctx, &IDQuery{ID: u.ID}, &userRename{Name: "新名"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := users.Get[IDQuery, user](tctx, &IDQuery{ID: u.ID})
	if err != nil || got == nil || got.Name != "新名" {
		t.Fatalf("更新后 Name=%v err=%v", got, err)
	}
	if got.Status != 0 {
		t.Fatalf("投影外的列不应被改动: status=%d", got.Status)
	}
	// 审计列经 Update 的 deff 刷新（curuser 恒为 auditUID，验证其已入库）。
	if got.UpdatedBy != auditUID {
		t.Fatalf("审计列未刷新: updated_by=%d", got.UpdatedBy)
	}

	// 空条件拒绝（omitCond 全零 → 取值后为空；IDQuery{} 的 id=0 是生效条件，不拦）。
	if err := users.Update[omitCond, userRename](tctx, &omitCond{}, &userRename{Name: "x"}); err == nil {
		t.Fatal("空条件更新应当报错")
	}
	// StatusRequest：零值也是有效值（清零）。
	if err := users.Update[IDQuery, StatusRequest[int]](tctx, &IDQuery{ID: u.ID}, &StatusRequest[int]{}); err != nil {
		t.Fatalf("StatusRequest 更新: %v", err)
	}
	got, _ = users.Get[IDQuery, user](tctx, &IDQuery{ID: u.ID})
	if got.Status != 0 {
		t.Fatalf("status 应保持 0，得到 %d", got.Status)
	}
}

// R8.3 主键剔除：投影含主键也不进 SET（主键集合只认 M）。
type userFullUpd struct {
	ID   int64  `dbx:"id"`
	Name string `dbx:"name"`
}

func TestUpdateDropsPK(t *testing.T) {
	_, users, _ := openUsers(t)
	u := &user{Name: "a"}
	mustCreate(t, users, u)
	if err := users.Update[IDQuery, userFullUpd](tctx, &IDQuery{ID: u.ID}, &userFullUpd{ID: 999, Name: "b"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := users.Get[IDQuery, user](tctx, &IDQuery{ID: u.ID})
	if got == nil || got.ID != u.ID || got.Name != "b" {
		t.Fatalf("主键不应被 SET 改动: %+v", got)
	}
}

// R9 Delete：命中删除；空条件拒绝；未命中不报错。
func TestDelete(t *testing.T) {
	_, users, _ := openUsers(t)
	u := &user{Name: "a"}
	mustCreate(t, users, u)
	if err := users.Delete[IDQuery](tctx, &IDQuery{ID: u.ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := users.Delete[omitCond](tctx, &omitCond{}); err == nil {
		t.Fatal("空条件删除应当报错")
	}
}

// R10 事务：Create 后返回错误回滚；嵌套复用。
func TestTx(t *testing.T) {
	db, users, _ := openUsers(t)
	err := db.Tx(tctx, func(ctx context.Context) error {
		if err := users.Create(ctx, &user{Name: "x"}); err != nil {
			return err
		}
		return fmt.Errorf("滚回去")
	})
	if err == nil || !strings.Contains(err.Error(), "滚回去") {
		t.Fatalf("应返回业务错误: %v", err)
	}
	n, listErr := users.Query[userCond, user](tctx, &userCond{})
	if listErr != nil || len(n) != 0 {
		t.Fatalf("回滚后应无记录: %v", listErr)
	}

	// 嵌套复用：内层不再开事务，最外层提交。
	err = db.Tx(tctx, func(ctx context.Context) error {
		return db.Tx(ctx, func(ctx context.Context) error {
			return users.Create(ctx, &user{Name: "y"})
		})
	})
	if err != nil {
		t.Fatalf("嵌套事务: %v", err)
	}
	n, _ = users.Query[userCond, user](tctx, &userCond{})
	if len(n) != 1 {
		t.Fatalf("外层提交后应有一条记录，得到 %d", len(n))
	}
}

// R12 重复注册 RegisterUser → panic。
func TestRegisterUserDup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复 RegisterUser 应当 panic")
		}
	}()
	RegisterUser(func(context.Context) (any, error) { return 1, nil })
}

func mustCreate(t *testing.T, users *Collection, u *user) {
	t.Helper()
	if err := users.Create(tctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
}
