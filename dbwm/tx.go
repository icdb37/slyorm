package dbwm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// 事务
// ---------------------------------------------------------------------------

// txKey 是事务在 context 里的键。
type txKey struct{}

// txState 是事务在 ctx 上的状态：事务句柄 + 是否已结束。
type txState struct {
	tx   Tx
	mu   sync.Mutex
	done bool
}

// isDone 报告事务是否已经结束（提交或回滚都算结束）。
func (s *txState) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// markDone 标记事务已结束：此后该 ctx 上的数据库调用一律报错。
func (s *txState) markDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
}

// txStateOf 取出 ctx 上绑定的事务状态；没有事务时返回 nil。
func txStateOf(ctx context.Context) *txState {
	st, _ := ctx.Value(txKey{}).(*txState)
	return st
}

// errTxDone 是"事务已结束"的哨兵错误。
var errTxDone = errors.New("dbwm: 事务已结束（已提交或回滚），该 ctx 上的数据库调用不再走连接池")

// Tx 在一个事务里执行 fn。
//
// fn 收到的 ctx 里带着事务，期间所有 dbwm 调用跑在同一个事务里。
// fn 返回错误 → 回滚并原样返回该错误；fn panic → 先回滚再把 panic 抛回去。
//
// 嵌套调用（fn 里再调 db.Tx）复用外层事务，由最外层统一提交或回滚。
//
// 事务结束后（提交或回滚都算结束），该 ctx 上的数据库调用报错 ——
// 绝不静默改走连接池，否则"以为在事务里"的写会悄悄落到池上。
func (db *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	if st := txStateOf(ctx); st != nil {
		if st.isDone() {
			return fmt.Errorf("dbwm: 事务已结束，不能再开启新事务")
		}
		return fn(ctx) // 已在事务里：复用，提交与回滚都归最外层
	}
	tx, err := db.conn.BeginTx(ctx)
	if err != nil {
		return db.fail("开启事务", err)
	}
	st := &txState{tx: tx}
	txCtx := context.WithValue(ctx, txKey{}, st)
	err = func() error {
		defer func() {
			if p := recover(); p != nil {
				st.markDone()
				_ = tx.Rollback()
				panic(p)
			}
		}()
		return fn(txCtx)
	}()
	st.markDone()
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			// 回滚失败也不能吞掉业务错误：join 起来，errors.Is 对两个分支都可穿透。
			return errors.Join(err, fmt.Errorf("dbwm: 回滚事务失败: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return db.fail("提交事务", err)
	}
	return nil
}

// execerOf 取出 ctx 绑定的执行器：事务优先；事务已结束则报错。
func (db *DB) execerOf(ctx context.Context) (Execer, error) {
	if st := txStateOf(ctx); st != nil {
		if st.isDone() {
			return nil, errTxDone
		}
		return st.tx, nil
	}
	return db.conn, nil
}

// querierOf 取出 ctx 绑定的查询器：事务优先；事务已结束则报错。
func (db *DB) querierOf(ctx context.Context) (Querier, error) {
	if st := txStateOf(ctx); st != nil {
		if st.isDone() {
			return nil, errTxDone
		}
		return st.tx, nil
	}
	return db.conn, nil
}
