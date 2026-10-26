// Package dbwm 是数据库访问中间层：把方言无关的抽象数据类型（IR）渲染成
// SQL 并执行。
//
// 上层（slyorm）只描述"要做什么查询" —— 表、列、条件树、排序、分组与聚合、
// 分页、写入计划；由本包渲染成某个数据库认的 SQL 并执行。
//
// 行数据由 RowFactory 落地：NewRow 返回一行各列的扫描地址（实现侧创建
// 行对象并收集），本包把地址交给驱动扫描、返回扫到的行数 —— 本包不认识
// 行类型、不带泛型，泛型包装在 slyorm 层，业务侧直接拿到 []*T：
//
//	var rows []*Person
//	f := dbwm.RowFactoryFunc(func() []any {
//		p := new(Person)
//		rows = append(rows, p)
//		return []any{&p.ID, &p.Name} // 与 Query.Columns 顺序一致
//	})
//	err := db.Qryall(ctx, &dbwm.Query{
//		Table:   "person",
//		Columns: []string{"id", "name"},
//		Where: &dbwm.Wnode{Items: []dbwm.Node{
//			&dbwm.Wcond{Column: "name", Op: dbwm.OpLike, Arg: "%张%"},
//		}},
//	}, f)
//	// rows 里是扫描完成的 *Person
//
// 约定：
//   - 值一律走绑定参数，不拼进 SQL 文本；
//   - 方言差异全部收在本包：标识符引用、占位符、分页写法、取回生成值的方式、
//     一条语句的参数上限；
//   - 本包不认识 dbx —— 行目标由上层按 RowFactory 提供，扫描交给驱动。
//
// 需求与规则见 docs/prd-dbwm.md。
//
// 文件分工：
//
//	dbwm.go      本文件 —— 包注释、句柄 DB 与构造入口
//	option.go    构造选项（Option / WithDialect）
//	ir.go        抽象数据类型（IR）
//	dialect.go   方言接口与 ANSI / MySQL / PostgreSQL 实现
//	err.go       错误类型：类别 Kind、类别哨兵、*Error、判定接口
//	errclass.go  驱动错误识别：方言识别规则与通用兜底、约束名提取
//	operator.go  算子表与算子注册
//	where.go     条件树 → SQL 片段与参数（括号规则、参数顺序）
//	sql.go       语句渲染骨架与各语句的渲染入口
//	sql_select.go SELECT 的子句拼装（列清单 / 分组 / 排序 / 分页）
//	querier.go   查询接口（Qryall / Qryone / Qrypgs）与行接收
//	exec.go      写入接口（Insert / Update / Delete）与批量分块
//	exec_script.go 脚本执行（ExecScript）：原样 SQL 文本的唯一入口
//	deps.go      依赖抽象（Conn / Tx）与 *sql.DB / *sql.Tx 的适配器
//	tx.go        事务（ctx 传递、嵌套复用）
//	detect.go    库型识别（驱动名映射 + 探测）
package dbwm

import (
	"context"
	"database/sql"
	"fmt"
)

// DB 是数据库句柄：连接 + 方言。
//
// 底层连接由调用方注入（见 New / NewSQL）或由本句柄打开（见 Open）：
// 本包只依赖 Conn 接口，*sql.DB 只是其中一种实现（见 deps.go）。
type DB struct {
	conn    Conn
	dialect Dialect
	// closer 非 nil 表示连接由本句柄打开 —— Close 时才真关；
	// 注入的连接所有权在注入方，dbwm 不代关。
	closer func() error
}

// Open 按驱动名打开一个数据库，并选定方言。
//
// driver 是已注册的驱动名（"sqlite"/"mysql"/"postgres"…），
// 调用方需要自己注册驱动（本包不依赖驱动）：
//
//	import _ "modernc.org/sqlite"
//
// 方言按驱动名映射（sqlite → ANSI、mysql → MySQL、postgres/pgx → PostgreSQL，
// 见 DialectForDriver）；映射不到就连库探测（见 Detect），认不出来报错 ——
// 不猜。用选项 WithDialect 显式指定方言时不再映射与探测。
//
// dsn 例子：":memory:"、"./app.db"、"file:app.db?cache=shared"。
//
// SQLite 的 ":memory:" 会被限制为单连接：内存库按连接独立，连接池新开的连接
// 看到的是空库。要多连接共享内存库，用 "file::memory:?cache=shared"
// 并保证库对象存活。
func Open(driver, dsn string, opts ...Option) (*DB, error) {
	base, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("dbwm: 打开数据库失败（驱动 %q 是否已注册）: %w", driver, err)
	}
	// SQLite 的 ":memory:" 限制为单连接 —— 必须早于探测设置，
	// 否则探测用的连接与后续语句用的连接可能不是同一个（各自一个空库）。
	if (driver == "sqlite" || driver == "sqlite3") && dsn == ":memory:" {
		base.SetMaxOpenConns(1)
	}
	conn := sqlConn{base}
	d := applyOptions(opts).dialect
	if d == nil {
		var ok bool
		d, ok = DialectForDriver(driver)
		if !ok {
			d, err = Detect(context.Background(), conn)
			if err != nil {
				_ = base.Close() // 探测失败别漏了连接池
				return nil, err
			}
		}
	}
	return &DB{conn: conn, dialect: d, closer: base.Close}, nil
}

// New 用调用方提供的连接构造句柄 —— 依赖注入的入口。
//
// conn 只要求实现 Conn 接口：可以传 *sql.DB 的适配（见 NewSQL），
// 也可以传自己的实现（连接池、事务包装、测试替身）。
//
// 方言用选项指定（见 WithDialect）；不给选项时用标准 SQL 方言（ANSI）。
// 连接是调用方给的，库型通常已知，这里不做探测。
func New(conn Conn, opts ...Option) *DB {
	o := applyOptions(opts)
	return &DB{conn: conn, dialect: OrDefaultDialect(o.dialect)}
}

// NewSQL 用已有的 *sql.DB 构造句柄（连接池由调用方负责）。
//
// 方言用选项指定（见 WithDialect）；不给选项时用标准 SQL 方言（ANSI），同 New。
func NewSQL(base *sql.DB, opts ...Option) *DB {
	o := applyOptions(opts)
	return &DB{conn: sqlConn{base}, dialect: OrDefaultDialect(o.dialect)}
}

// Dialect 返回本句柄使用的 SQL 方言。
func (db *DB) Dialect() Dialect { return db.dialect }

// Close 关闭底层连接。
//
// 只关闭 Open 打开的连接；New / NewSQL 注入的连接所有权在注入方，本方法不代关。
func (db *DB) Close() error {
	if db.closer == nil {
		return nil
	}
	return db.closer()
}
