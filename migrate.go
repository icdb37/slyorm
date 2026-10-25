package slyorm

import (
	"context"
	"io/fs"

	"github.com/icdb37/slyorm/migration"
)

// Migrate 按水位执行业务提供的 SQL 脚本目录（建表、初始化、结构修补），
// 已执行过的不重复执行，执行记录落在 sys_migration 表。
//
// fsys 是业务 embed 的脚本目录（如 //go:embed v1.0/*.sql 的 embed.FS），
// 脚本文件名形如 {版本号}_{序号}_{名称}.sql，按（版本号, 序号）升序执行。
// 返回执行统计（Scripts 装载数 / Applied 本次执行数），日志由业务自己记。
//
// 可在 Tx 内调用（复用外层事务，语义见 prd-migration R5.5）。其余语义
// （水位、失败即停与重试、留痕、空目录零副作用）见 docs/prd-migration.md。
func (db *DB) Migrate(ctx context.Context, fsys fs.FS) (migration.Stats, error) {
	return migration.Run(ctx, db.wm, fsys)
}
