// Package migration 数据迁移：按水位执行业务提供的 SQL 脚本目录（建表、
// 初始化、结构修补），执行记录落在 sys_migration 表，已执行过的不重复执行。
//
// 脚本目录由业务 embed 提供并传入（fs.FS，如 //go:embed v1.0/*.sql；装载时
// 递归收集根下全部 .sql），脚本文件名形如 {版本号}_{序号}_{名称}.sql。
// 执行经 dbwm：脚本文本走 ExecScript，留痕与水位走 IR，一脚本一事务 ——
// 本包不 import database/sql、不持连接。
//
// 需求与规则见 docs/prd-migration.md。
//
// 文件分工：
//
//	migration.go 本文件 —— 包注释与对外 API：Run（装载 → 建表 → 水位 → 逐脚本执行留痕）
//	script.go    脚本装载：文件名解析、递归遍历、排序与解析期校验（格式 / 字符集 / 查重）
//	record.go    执行记录：列名常量与建表 DDL、留痕（Insert IR）、水位查询（Query IR）
package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/icdb37/slyorm/dbwm"
)

// Stats 一次迁移的执行统计。
type Stats struct {
	// Scripts 装载到的脚本总数。
	Scripts int
	// Applied 本次实际执行的脚本数（跳过不计；出错时是失败前已成功数）。
	Applied int
}

// Run 执行水位之后的脚本：先比版本号、再比序号（R4）。
//
// 任一脚本失败即停止并返回错误（失败即停是失败可重试的前提，R4.3），
// 其后的脚本留待下次执行；失败脚本在脚本事务之外留 success=0 的记录
// （失败原因入库）。空目录零副作用：不建表、不发任何 SQL（R1.4）。
//
// 可在 db.Tx 内调用：复用外层事务，全部脚本与留痕由外层统一提交或回滚
// —— 失败脚本已执行的部分语句留在外层事务里，本层无法撤销，因此 Run
// 的错误必须冒泡到外层（吞掉再提交等于提交半截脚本，R5.5）。
func Run(ctx context.Context, db *dbwm.DB, fsys fs.FS) (Stats, error) {
	if db == nil {
		return Stats{}, fmt.Errorf("migration: dbwm 句柄不能为空")
	}
	if fsys == nil {
		return Stats{}, fmt.Errorf("migration: 脚本目录（fs.FS）不能为空")
	}
	// R2.5 装载与校验先于一切数据库操作：装载失败零副作用。
	scripts, err := load(fsys)
	if err != nil {
		return Stats{}, err
	}
	if len(scripts) == 0 {
		return Stats{}, nil
	}
	if err := ensureTable(ctx, db); err != nil {
		return Stats{}, err
	}
	version, sequence, err := watermark(ctx, db)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{Scripts: len(scripts)}
	for _, sc := range scripts {
		if !sc.after(version, sequence) {
			continue
		}
		if err := execScript(ctx, db, sc); err != nil {
			return stats, err
		}
		stats.Applied++
	}
	return stats, nil
}

// execScript 执行单个脚本并留痕（R5）：脚本与成功记录同事务提交；
// 失败则事务回滚后在该事务之外留失败记录，错误 join 上抛（R5.3）。
func execScript(ctx context.Context, db *dbwm.DB, sc *script) error {
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := db.ExecScript(ctx, sc.body); err != nil {
			return fmt.Errorf("migration: 执行脚本 %s 失败: %w", sc.file, err)
		}
		return record(ctx, db, sc, true, "")
	})
	if err == nil {
		return nil
	}
	// R5.2 失败留痕必须落库：脚本已回滚，库上无痕迹，这条记录是唯一线索。
	// ctx 已失效时写不进属预期（R8.2）：join 上抛，不吞、不盖掉脚本错误。
	if rerr := record(ctx, db, sc, false, err.Error()); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}
