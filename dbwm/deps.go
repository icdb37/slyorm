package dbwm

import (
	"context"
	"database/sql"
)

// ---------------------------------------------------------------------------
// 依赖抽象
//
// 本包不直接依赖 *sql.DB：对数据库的调用一律走下面的接口，
// 具体实现（*sql.DB、别的连接池、测试替身）由调用方注入。
// ---------------------------------------------------------------------------

// Execer 执行一条带参数的语句。
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Querier 执行查询。
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx 是事务：执行语句、查询、提交、回滚。
type Tx interface {
	Execer
	Querier
	Commit() error
	Rollback() error
}

// Conn 是数据库连接 —— 依赖倒置的落点。
//
// 本包只按接口调用 Conn，不关心它背后是什么：默认实现是 *sql.DB
// （见 NewSQL / Open），想换连接池、想注入 mock、想断言执行了哪条 SQL，
// 都只需换一个实现了 Conn 的类型。
type Conn interface {
	Execer
	Querier
	// BeginTx 开启一个事务。
	BeginTx(ctx context.Context) (Tx, error)
}

// sqlConn 把 *sql.DB 适配成 Conn：具体驱动只在这一层出现。
type sqlConn struct {
	*sql.DB
}

// BeginTx 开启事务，并把 *sql.Tx 适配成 Tx。
//
// 这里显式定义 BeginTx，覆盖内嵌 *sql.DB 的同名方法：
// 签名不同（不收 opts，返回 Tx 而不是 *sql.Tx）。
func (c sqlConn) BeginTx(ctx context.Context) (Tx, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return sqlTx{tx}, nil
}

// sqlTx 把 *sql.Tx 适配成 Tx。
type sqlTx struct {
	*sql.Tx
}
