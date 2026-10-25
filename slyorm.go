// Package slyorm 是业务门面：表访问器 Collection（单表增删改查、分页列表）。
//
// 需求与规则见 docs/prd-slyorm.md。分层（见 docs/prd.md）：
// business → slyorm → dbwm → sql.DB —— 业务给"表模型 + 条件结构体"，
// 门面组织 model / query / insert / select（rows）各能力产出 dbwm 的 IR 并执行，
// 业务拿到 *T / []*T。
//
// 类型三层归属（目录隔离，见 prd-slyorm §3）：与 dbwm 直接共享的 IR 类型
// 唯一定义在 dbwm（本包直接引用、不转发）；业务侧数据类型（分页协议、
// IDQuery 一族、Range 再导出）在根包；模型元信息在 model 包。
//
// 建表不在范围：表结构由业务手写 SQL 建好（要自己执行建表 SQL 的用注入形态
// NewSQL 并保留 *sql.DB 句柄）。
//
// 文件分工：
//
//	slyorm.go     本文件 —— 句柄 DB（转发 dbwm）与包级说明
//	collection.go Collection 的对外 API：构造与 Get/Query/Xget/Xqry/List/Create/Creates/Update/Delete
//	collection_helper.go Collection 的辅助实现：条件构造、投影列校验、读流程、SET 主键剔除
//	list.go       List 与分页协议类型（PageQuery/ListRequest/ListResponse）
//	types.go      通用数据库类型：Range 再导出、IDQuery / IDsQuery / CodeQuery / StatusRequest
//	audit.go      RegisterUser 与 curuser 默认值（审计列）
//	migrate.go    Migrate：初始化脚本执行（转发 migration.Run，见 prd-migration）
package slyorm

import (
	"context"
	"database/sql"

	"github.com/icdb37/slyorm/dbwm"
)

// DB 是门面句柄：连接 + 方言，全部由 dbwm 管理。
//
// 构造入口与错误语义同 dbwm（库型识别、方言、事务、:memory: 单连接限制）；
// 本包不另设连接抽象、不提供取回底层 *sql.DB 的逃生舱。
type DB struct {
	wm *dbwm.DB
}

// Open 按驱动名打开数据库并选定方言。
//
// driver 是已注册的驱动名，调用方需要自己注册驱动（本库不依赖驱动）：
//
//	import _ "modernc.org/sqlite"
//
// 方言按驱动名映射，映射不到就连库探测；用选项显式指定方言时不再映射与探测
// （见 dbwm.WithDialect）。
func Open(driver, dsn string, opts ...dbwm.Option) (*DB, error) {
	wm, err := dbwm.Open(driver, dsn, opts...)
	if err != nil {
		return nil, err
	}
	return &DB{wm: wm}, nil
}

// New 用调用方提供的连接构造句柄 —— 依赖注入的入口（要自持 *sql.DB 执行
// 建表 SQL 的走 NewSQL，句柄留在自己手里）。
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

// Close 关闭底层连接（只关 Open 打开的；注入的连接所有权在注入方）。
func (db *DB) Close() error { return db.wm.Close() }

// Tx 在一个事务里执行 fn（ctx 传递、嵌套复用、结束后该 ctx 上的调用报错，
// 语义见 prd-dbwm R9.4）。Collection 的全部方法感知事务。
func (db *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	return db.wm.Tx(ctx, fn)
}
