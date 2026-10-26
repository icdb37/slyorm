package insert

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/icdb37/slyorm/dbwm"

	_ "modernc.org/sqlite"
)

// ctx 测试用上下文。
var tctx = context.Background()

// 开库：内存 SQLite，**自持 *sql.DB**（建表 SQL 在它上面执行，PRD：注入方保留句柄），
// 经 NewSQL 注入写侧句柄。内存库限制单连接，避免连接池各见空库。
func openDB(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	base.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = base.Close() })
	return NewSQL(base), base
}

// person 表的建表 SQL（建表由业务手写，见 prd-slyorm §1.3）。
const personDDL = `CREATE TABLE person (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	age INT,
	addr_city TEXT NOT NULL,
	addr_street TEXT NOT NULL,
	payload TEXT NOT NULL,
	nick TEXT,
	created_at TIMESTAMP NOT NULL
)`

// 写规则测试模型。
type Address struct {
	City   string `dbx:"city"`
	Street string `dbx:"street"`
}
type person struct {
	ID        int64          `dbx:"id,autopk"`
	Name      string         `dbx:"name"`
	Age       int            `dbx:"age,null"`
	Address   Address        `dbx:"addr_,inline"`
	Payload   map[string]any `dbx:"payload,json"`
	Nick      *string        `dbx:"nick"`
	CreatedAt time.Time      `dbx:"created_at,deff=now"`
	Remark    string         `dbx:"-"`
	Ignored   string
}

// R4/R6/R3 IR 形态：列清单静态、autopk 零值跳列、null→NULL、json 序列化、nil 指针→NULL。
func TestBuildInsertIR(t *testing.T) {
	p := &person{Name: "张三"}
	ir, err := BuildInsertIR(tctx, p)
	if err != nil {
		t.Fatalf("BuildInsertIR: %v", err)
	}
	wantCols := []string{"name", "age", "addr_city", "addr_street", "payload", "nick", "created_at"}
	if strings.Join(ir.Columns, ",") != strings.Join(wantCols, ",") {
		t.Fatalf("列清单 =\n%v\n期望\n%v", ir.Columns, wantCols)
	}
	if ir.AutoPKColumn != "id" {
		t.Fatalf("自增列 = %q，期望 id", ir.AutoPKColumn)
	}
	row := ir.Rows[0]
	// age 标了 null 且为零 → SQL NULL（nil 参数）。
	if row[1] != nil {
		t.Fatalf("age 零值 + null 应写 NULL，得到 %v", row[1])
	}
	// json 零值也序列化写入（nil map → "null"）。
	if s, ok := row[4].(string); !ok || s != "null" {
		t.Fatalf("json 列参数 = %#v，期望序列化文本 \"null\"", row[4])
	}
	// Nick 是 nil 指针 → NULL。
	if row[5] != nil {
		t.Fatalf("nil 指针应写 NULL，得到 %v", row[5])
	}
	// created_at 由 deff=now 补齐并写回结构体。
	if p.CreatedAt.IsZero() {
		t.Fatal("deff=now 应把默认值写回结构体")
	}
}

// R6 自增主键：显式赋值则进列清单；回填只写零值字段。
func TestAutoPKExplicit(t *testing.T) {
	p := &person{ID: 42, Name: "李四"}
	ir, err := BuildInsertIR(tctx, p)
	if err != nil {
		t.Fatalf("BuildInsertIR: %v", err)
	}
	if ir.AutoPKColumn != "" {
		t.Fatalf("显式赋值的自增列不应取回，AutoPKColumn = %q", ir.AutoPKColumn)
	}
	if ir.Columns[0] != "id" {
		t.Fatalf("显式赋值的自增列应进列清单，得到 %v", ir.Columns)
	}
	if err := FillAutoID(p, 99); err != nil {
		t.Fatalf("FillAutoID: %v", err)
	}
	if p.ID != 42 {
		t.Fatalf("显式赋值不应被回填覆盖，ID = %d", p.ID)
	}
}

// badJSON 的 json 列类型序列化不出（map 的键不是字符串 / 整数）。
type badJSON struct {
	ID   int             `dbx:"id,autopk"`
	Data map[bool]string `dbx:"data,json"`
}

// R3⑨/prd-error R5.1 json 列序列化失败：值放不进 json 列 → 带 ErrTypeMismatch
// 哨兵；发生在值到数据库之前，不算数据库错误（ErrDatabase 不命中）。
func TestJSONSerializeFailCarriesKind(t *testing.T) {
	_, err := BuildInsertIR(tctx, &badJSON{Data: map[bool]string{true: "a"}})
	if err == nil {
		t.Fatal("map[bool]string 序列化不出该报错")
	}
	if !errors.Is(err, dbwm.ErrTypeMismatch) {
		t.Fatalf("错误 = %v，期望带 ErrTypeMismatch 哨兵", err)
	}
	if got := dbwm.KindOf(err); got != dbwm.KindTypeMismatch {
		t.Fatalf("类别 = %v，期望 %v", got, dbwm.KindTypeMismatch)
	}
	if errors.Is(err, dbwm.ErrDatabase) {
		t.Fatalf("值没到数据库，不该是数据库错误: %v", err)
	}
	if !strings.Contains(err.Error(), "Data") {
		t.Fatalf("错误 = %v，期望指出字段名", err)
	}
}

// R5 defv 字面量：解析期按字段类型校验。
type defvBad struct {
	Age int `dbx:"age,defv=abc"`
}

func TestDefvParseError(t *testing.T) {
	if _, err := BuildInsertIR(tctx, &defvBad{}); err == nil {
		t.Fatal("defv=abc 标在 int 字段上应当报错")
	}
}

// R11 语义冲突：deff+defv、json+null、autopk+null。
type conflictA struct {
	A int `dbx:"a,deff=now,defv=1"`
}
type conflictB struct {
	A map[string]any `dbx:"a,json,null"`
}
type conflictC struct {
	A int64 `dbx:"a,autopk,null"`
}

func TestOptionConflicts(t *testing.T) {
	for _, v := range []any{&conflictA{}, &conflictB{}, &conflictC{}} {
		if _, err := BuildInsertIR(tctx, v); err == nil {
			t.Fatalf("%T 的选项冲突应当报错", v)
		}
	}
}

// R5.6 / prd-slyorm R8.2 Update IR：全列进 SET，deff 生效、defv 不生效。
type updModel struct {
	Name string    `dbx:"name"`
	Age  int       `dbx:"age,defv=18"`
	At   time.Time `dbx:"updated_at,deff=now"`
}

func TestBuildUpdateIR(t *testing.T) {
	m := &updModel{Name: "新名"}
	up, err := BuildUpdateIR(tctx, m)
	if err != nil {
		t.Fatalf("BuildUpdateIR: %v", err)
	}
	if strings.Join(up.Columns, ",") != "name,age,updated_at" {
		t.Fatalf("SET 列 = %v", up.Columns)
	}
	// Age 零值：defv 不生效 → 写 Go 零值 0。
	if up.Values[1] != 0 {
		t.Fatalf("defv 在 Update 不生效，age 应写 0，得到 %v", up.Values[1])
	}
	// updated_at 零值：deff 生效 → 补默认值并写回。
	if m.At.IsZero() {
		t.Fatal("deff 在 Update 应补默认值并写回结构体")
	}
}

// R10 omit：零值列不进 SET（写侧只在 UPDATE 生效，INSERT 不受影响）。
type updOmit struct {
	Name string    `dbx:"name,omit"`
	Age  int       `dbx:"age,omit,null"`
	Nick *string   `dbx:"nick,omit"`
	At   time.Time `dbx:"at,omit,deff=now"`
}

func TestBuildUpdateIROmit(t *testing.T) {
	blank := ""
	m := &updOmit{Name: "新名", Nick: &blank}
	up, err := BuildUpdateIR(tctx, m)
	if err != nil {
		t.Fatalf("BuildUpdateIR: %v", err)
	}
	// name 非零 → 进 SET；age 零值（omit 优先于 null）→ 不进；
	// nick 指向空串（显式赋过零值）→ 进 SET；at 有 deff，补完非零 → 进 SET。
	if strings.Join(up.Columns, ",") != "name,nick,at" {
		t.Fatalf("SET 列 = %v，期望 name,nick,at", up.Columns)
	}
	if s, ok := up.Values[1].(string); !ok || s != "" {
		t.Fatalf("nick 应写空串，得到 %#v", up.Values[1])
	}
}

func TestBuildUpdateIROmitZero(t *testing.T) {
	up, err := BuildUpdateIR(tctx, &updOmit{})
	if err != nil {
		t.Fatalf("BuildUpdateIR: %v", err)
	}
	// 零值的 omit 列一律不进 SET，只有 deff 补过值的 at 在。
	if strings.Join(up.Columns, ",") != "at" {
		t.Fatalf("SET 列 = %v，期望只有 at", up.Columns)
	}
}

// R10.3 INSERT 不受影响：列清单恒定，omit 列照常写入。
func TestInsertOmitsIgnored(t *testing.T) {
	ir, err := BuildInsertIR(tctx, &updOmit{})
	if err != nil {
		t.Fatalf("BuildInsertIR: %v", err)
	}
	if strings.Join(ir.Columns, ",") != "name,age,nick,at" {
		t.Fatalf("INSERT 列清单 = %v，期望 name,age,nick,at", ir.Columns)
	}
}

// R10.6 列全部被 omit 跳过 → 没有可更新的列，报错。
type updOmitAll struct {
	Name string `dbx:"name,omit"`
}

func TestBuildUpdateIROmitAll(t *testing.T) {
	if _, err := BuildUpdateIR(tctx, &updOmitAll{}); err == nil {
		t.Fatal("全部列被 omit 跳过时应当报错")
	}
}

// R9 批量：自增列整批零值或整批显式，混用报错；自增值按行序回填。
func TestInsertAll(t *testing.T) {
	db, base := openDB(t)
	if _, err := base.ExecContext(tctx, personDDL); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	people := []*person{{Name: "a"}, {Name: "b"}}
	if _, err := db.InsertAll(tctx, &people); err != nil {
		t.Fatalf("InsertAll: %v", err)
	}
	if people[0].ID != 1 || people[1].ID != 2 {
		t.Fatalf("自增值按行序回填失败: %d %d", people[0].ID, people[1].ID)
	}

	mixed := []*person{{Name: "a"}, {ID: 9, Name: "b"}}
	if _, err := db.InsertAll(tctx, &mixed); err == nil {
		t.Fatal("自增列混用（零值与显式）应当报错")
	}
}

// R6 执行与回填（真实 SQLite）：插入零值自增主键，成功后 ID 可用。
func TestInsertExec(t *testing.T) {
	db, base := openDB(t)
	if _, err := base.ExecContext(tctx, personDDL); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	p := &person{Name: "王五", Age: 30}
	res, err := db.Insert(tctx, p)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if res.RowsAffected != 1 || p.ID == 0 {
		t.Fatalf("插入结果 = %+v，p.ID = %d", res, p.ID)
	}

	// 读回验证落库形态：age 非零写原值，nick 为 NULL。
	var age int
	var nick any
	if err := base.QueryRowContext(tctx, `SELECT age, nick FROM person WHERE id = ?`, p.ID).Scan(&age, &nick); err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if age != 30 || nick != nil {
		t.Fatalf("读回 age=%v nick=%v，期望 30 / NULL", age, nick)
	}
}

// BuildInsert 只生成语句不落库；语句与 dbwm 执行路径的渲染**逐字一致**
// （同一份渲染器，见 prd-slyorm §3 评审 F6）。
func TestBuildInsert(t *testing.T) {
	db, _ := openDB(t)
	p := &person{Name: "赵六"}
	stmt, args, err := db.BuildInsert(tctx, p)
	if err != nil {
		t.Fatalf("BuildInsert: %v", err)
	}
	ir, err := BuildInsertIR(tctx, &person{Name: "赵六"})
	if err != nil {
		t.Fatalf("BuildInsertIR: %v", err)
	}
	ir.Table = "person"
	wantStmt, wantArgs, err := dbwm.RenderInsert(db.Dialect(), ir)
	if err != nil {
		t.Fatalf("dbwm.RenderInsert: %v", err)
	}
	if stmt != wantStmt || len(args) != len(wantArgs) {
		t.Fatalf("BuildInsert 语句 =\n%s\n与 dbwm 渲染 =\n%s\n不一致", stmt, wantStmt)
	}
	if !strings.HasPrefix(stmt, `INSERT INTO "person"("name"`) || !strings.HasSuffix(stmt, ")") {
		t.Fatalf("语句 = %s", stmt)
	}
	if n := strings.Count(stmt, "?"); n != len(args) {
		t.Fatalf("参数 %d 与占位符数 %d 不符: %s", len(args), n, stmt)
	}
}

// 注册表纪律：自定义选项重名 → panic（评审 F8）。
func TestRegisterOptionDup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册自定义选项应当 panic")
		}
	}()
	RegisterOption("dupopt", func(*Column, string) error { return nil })
	RegisterOption("dupopt", func(*Column, string) error { return nil })
}

// 评审 F5：IR 构造不依赖表名 —— 匿名结构体投影可用（表名由调用方填）。
func TestAnonymousProjectionIR(t *testing.T) {
	p := &struct {
		Name string `dbx:"name"`
	}{Name: "匿名"}
	ir, err := BuildInsertIR(tctx, p)
	if err != nil {
		t.Fatalf("匿名结构体投影 BuildInsertIR: %v", err)
	}
	if ir.Table != "" || len(ir.Columns) != 1 || ir.Columns[0] != "name" {
		t.Fatalf("IR = %+v，期望 Table 空 + 单列 name", ir)
	}
	ir.Table = "person" // 调用方（门面）填表名
}

// 默认值注册表：重复注册 panic。
func TestRegisterDefaultDup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册默认值函数应当 panic")
		}
	}()
	RegisterDefault("dupfn", func(context.Context) (any, error) { return 1, nil })
	RegisterDefault("dupfn", func(context.Context) (any, error) { return 2, nil })
}

// 句柄构造：方言走构造选项（dbwm.WithDialect），不给选项时是 ANSI。
func TestNewSQLDialectOption(t *testing.T) {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	if got := NewSQL(base).Dialect().Name(); got != dbwm.ANSI.Name() {
		t.Fatalf("不给选项的方言 = %s，期望 %s", got, dbwm.ANSI.Name())
	}
	if got := NewSQL(base, dbwm.WithDialect(dbwm.MySQL)).Dialect().Name(); got != dbwm.MySQL.Name() {
		t.Fatalf("带选项的方言 = %s，期望 %s", got, dbwm.MySQL.Name())
	}
}
