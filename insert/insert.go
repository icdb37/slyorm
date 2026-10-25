// Package insert 实现数据写入（INSERT / UPDATE 的值成形与 IR 构造）。
//
// 需求与规则见 docs/prd-insert.md；UPDATE 的取值语义（deff 生效、defv 不生效）
// 见 prd-slyorm R8。列名推导与静态列集合在 model 包（唯一入口）；
// 语句渲染与执行在 dbwm —— 本包负责**写规则解释与值成形**，产出
// `*dbwm.Insert` / `*dbwm.Update`，门面（slyorm）复用这些 IR 构造入口。
//
// 一个结构体 + 一个 dbx 标签描述一张表的写入行为：
//
//	type Person struct {
//		ID      int64   `dbx:"id,autopk"`     // 自增主键：零值不进 INSERT，成功后回填
//		Name    string  `dbx:"name"`           // 零值写 Go 零值
//		Age     int     `dbx:"age,null"`      // 零值写 SQL NULL
//		Address Address `dbx:"addr_,inline"`  // 展开成 addr_city、addr_screet
//		Payload Extra   `dbx:"payload,json"`  // 整个值存成一列 JSON
//		Remark  string  `dbx:"-"`             // 完全不参与
//	}
//
//	BuildInsertIR(ctx, &p)    // 模型 → *dbwm.Insert（列清单 + 行值 + 自增列）
//	BuildUpdateIR(ctx, &p)    // 模型 → *dbwm.Update（SET 列与值，deff 补零值）
//	db.Insert(ctx, &p)        // 插入一条（表名走 TableName()/RegisterTable/类型名）
//	db.InsertAll(ctx, people) // 批量（多 VALUES；超参数上限由 dbwm 自动分块）
//
// 核心规则：**除了 dbx:"-" 的字段与零值的自增列，所有列都参与 INSERT**。
//
// 数据库访问全部走 dbwm（连接、方言、事务、执行）；本包不 import 任何驱动。
//
// 文件分工：
//
//	insert.go  本文件 —— 对外 API（句柄、IR 构造入口、执行入口）
//	model.go   写侧模型：表名机制与列的写规则解释（挂在 model 包的静态列上）
//	parse.go   静态列 → 写侧列（选项校验、defv 字面量解析）
//	plan.go    写入计划（值成形、默认值、自增跳列）与语句渲染
//	exec.go    执行与自增值写回
//	value.go   取值与写回
//	default.go 默认值（函数与字面量）注册表
//	option.go  写规则的选项名与自定义选项注册表
package insert

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"slyorm/dbwm"
)

// ---------------------------------------------------------------------------
// 句柄
// ---------------------------------------------------------------------------

// DB 是写侧句柄：包装 dbwm 句柄，提供"模型直入"的插入入口。
//
// 连接、方言、事务全部由 dbwm 管理（见 dbwm.DB）；本包只解释写规则。
type DB struct {
	wm *dbwm.DB
}

// Open 按驱动名打开数据库（转发 dbwm.Open：驱动名映射方言，映射不到连库探测，
// 认不出报错不猜）。驱动由调用方注册（`_ "modernc.org/sqlite"`）。
func Open(driver, dsn string, opts ...dbwm.Option) (*DB, error) {
	wm, err := dbwm.Open(driver, dsn, opts...)
	if err != nil {
		return nil, err
	}
	return &DB{wm: wm}, nil
}

// New 用调用方提供的连接构造句柄（依赖注入入口）。
//
// 方言用选项指定（见 dbwm.WithDialect）；不给选项时用标准 SQL 方言（ANSI）。
func New(conn dbwm.Conn, opts ...dbwm.Option) *DB {
	return &DB{wm: dbwm.New(conn, opts...)}
}

// NewSQL 用已有的 *sql.DB 构造句柄（连接池由调用方负责）。
//
// 方言用选项指定（见 dbwm.WithDialect）；不给选项时用标准 SQL 方言（ANSI）。
func NewSQL(base *sql.DB, opts ...dbwm.Option) *DB {
	return &DB{wm: dbwm.NewSQL(base, opts...)}
}

// Dialect 返回句柄使用的方言。
func (db *DB) Dialect() dbwm.Dialect { return db.wm.Dialect() }

// Close 关闭底层连接（只关 Open 打开的，注入的不代关，语义同 dbwm）。
func (db *DB) Close() error { return db.wm.Close() }

// Tx 在一个事务里执行 fn（ctx 传递、嵌套复用，语义见 dbwm R9.4）。
func (db *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	return db.wm.Tx(ctx, fn)
}

// Result 一次插入的结果。
type Result struct {
	// RowsAffected 影响行数。
	RowsAffected int64
	// LastInsertID 数据库生成的最后一个自增值；没有自增列时为 0。
	LastInsertID int64
}

// ---------------------------------------------------------------------------
// IR 构造入口（门面复用；执行走 dbwm）
// ---------------------------------------------------------------------------

// BuildInsertIR 构造一条单行插入的 IR：列清单 + 行值 + 自增列。
//
// **Table 留空由调用方填**（门面填 Collection 绑定的表名；本包独立执行路径
// 填表名机制推导的结果）—— IR 构造不依赖表名，投影可以是匿名结构体。
// 默认值函数（deff=）在此执行并写回结构体；自增列零值不进列清单
//（AutoPKColumn 记下它），显式赋值则照常写入。
func BuildInsertIR(ctx context.Context, v any) (*dbwm.Insert, error) {
	elem, m, err := prepareWrite(v)
	if err != nil {
		return nil, err
	}
	p, err := buildPlan(ctx, m, elem)
	if err != nil {
		return nil, err
	}
	return p.insertIR(m), nil
}

// BuildInsertAllIR 构造批量插入的 IR：多行共用一份列清单。
//
// 自增列要么整批都是零值（交给数据库生成），要么整批都显式赋值，
// 混用报错（prd-insert R9.3）。分块由 dbwm 按参数上限自动处理。
func BuildInsertAllIR(ctx context.Context, vs any) (*dbwm.Insert, error) {
	rv, err := sliceOf(vs)
	if err != nil {
		return nil, err
	}
	if rv.Len() == 0 {
		return nil, fmt.Errorf("insert: 没有可插入的行")
	}
	ir, _, err := buildInsertAll(ctx, rv)
	return ir, err
}

// BuildUpdateIR 构造更新的 IR：SET 的列与值。
//
// **Table 与 Where 留空由调用方填**（门面填 Collection 的表名与条件树）。
// 值成形与插入同一套（null 零值写 NULL、nil 指针写 NULL、json 序列化），
// 差异是 **deff= 生效**（零值时补默认值并写回，审计列刷新的落点，见
// prd-slyorm R8）、**defv= 不生效**；自增列照常进 SET（主键剔除由门面按
// 表模型 M 的主键集合做，见 prd-slyorm R8.3）。
func BuildUpdateIR(ctx context.Context, v any) (*dbwm.Update, error) {
	elem, m, err := prepareWrite(v)
	if err != nil {
		return nil, err
	}
	p, err := buildUpdatePlan(ctx, m, elem)
	if err != nil {
		return nil, err
	}
	return &dbwm.Update{Columns: p.columns, Values: p.values}, nil
}

// FillAutoID 把数据库生成的自增值写回模型（只写本来是零值的字段，
// 见 prd-insert R6.6）。
//
// 自增列由模型自己认（dbx 标签里的 autopk）—— 调用方不必再传列名；
// v 是被写入的模型（指针）。批量时逐个元素调用、各传一个 id。
func FillAutoID(v any, id int64) error {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	m, err := writeModelOf(rv.Type())
	if err != nil {
		return err
	}
	if m.autoPK == nil {
		return nil
	}
	return setAutoPK(m.autoPK, rv, id)
}

// ---------------------------------------------------------------------------
// 执行入口
// ---------------------------------------------------------------------------

// Insert 插入一条记录：构造 IR 并填上表名（走表名机制，匿名结构体在此报错）
// 交给 dbwm 执行，自增值从 dbwm.Result.IDs 取回并写回结构体。
func (db *DB) Insert(ctx context.Context, v any) (Result, error) {
	elem, m, err := prepare(v)
	if err != nil {
		return Result{}, err
	}
	p, err := buildPlan(ctx, m, elem)
	if err != nil {
		return Result{}, err
	}
	return db.exec(ctx, p.insertIR(m), []any{v})
}

// InsertAll 批量插入切片里的多条记录。
//
// 多 VALUES 一条语句，超参数上限由 dbwm 自动分块；分块产生的多条语句
// **不自动包事务**（要么包 db.Tx，要么接受部分成功，见 prd-insert R9.7）。
func (db *DB) InsertAll(ctx context.Context, vs any) (Result, error) {
	rv, err := sliceOf(vs)
	if err != nil {
		return Result{}, err
	}
	if rv.Len() == 0 {
		return Result{}, nil
	}
	ir, _, err := buildInsertAll(ctx, rv)
	if err != nil {
		return Result{}, err
	}
	// 独立执行路径在这里填表名（走表名机制；匿名结构体在此报错）。
	first, err := structAt(rv, 0)
	if err != nil {
		return Result{}, err
	}
	named, err := ModelOf(first.Type())
	if err != nil {
		return Result{}, err
	}
	ir.Table = named.TableRef()
	// items 里是解引用后的各行值；写回自增时要经指针改字段，取原入参的元素。
	wrappers := make([]any, rv.Len())
	for i := range wrappers {
		wrappers[i] = rv.Index(i).Addr().Interface() // 元素是 *T 时 Addr 再解引用也指向同一对象
	}
	return db.exec(ctx, ir, wrappers)
}

// BuildInsert 只生成 INSERT 语句与参数，不落库（默认值函数仍会执行并写回结构体）。
//
// 语句由 dbwm 的渲染入口产出 —— 与执行路径同一份渲染器，预览与执行的
// 文本一致（见 prd-slyorm §3 的切口：语法归 dbwm）。
func (db *DB) BuildInsert(ctx context.Context, v any) (string, []any, error) {
	elem, m, err := prepare(v)
	if err != nil {
		return "", nil, err
	}
	p, err := buildPlan(ctx, m, elem)
	if err != nil {
		return "", nil, err
	}
	return dbwm.RenderInsert(db.wm.Dialect(), p.insertIR(m))
}

// exec 执行 IR 并把自增值写回每个模型元素。
func (db *DB) exec(ctx context.Context, ir *dbwm.Insert, vs []any) (Result, error) {
	res, err := db.wm.Insert(ctx, ir)
	if err != nil {
		return Result{}, err
	}
	out := Result{RowsAffected: res.RowsAffected}
	if len(res.IDs) > 0 {
		out.LastInsertID = res.IDs[len(res.IDs)-1]
		if err := fillIDs(vs, res.IDs); err != nil {
			return out, err
		}
	}
	return out, nil
}

// fillIDs 按行序把生成值写回各行模型。
func fillIDs(vs []any, ids []int64) error {
	for i, v := range vs {
		if i >= len(ids) {
			break
		}
		if err := FillAutoID(v, ids[i]); err != nil {
			return err
		}
	}
	return nil
}
