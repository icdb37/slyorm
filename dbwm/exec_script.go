package dbwm

import (
	"context"
	"strings"
)

// ---------------------------------------------------------------------------
// 脚本执行
// ---------------------------------------------------------------------------

// ExecScript 原样执行一段 SQL 文本（DDL / 初始化脚本）。
//
// 不渲染、不拆分、无绑定参数 —— 多语句整发，能否执行取决于驱动
// （SQLite 与 PostgreSQL 支持多语句，MySQL 需 DSN 带 multiStatements=true）。
// ctx 带事务时在事务内执行（R9.4）。空白文本直接返回 nil、不发语句
// （MySQL 对空查询报 1065）。返回值只有 error：多语句的 RowsAffected
// 不可靠，不暴露。
//
// 这是"没有原始 SQL 逃生舱"（§8.10）与 R9.8"不做 DDL"的窄修订（R11）：
// 脚本是声明式迁移能力（docs/prd-migration.md R6）的一部分，经此唯一
// 入口执行 —— 不是业务查询绕过 IR 拼条件的口子。
func (db *DB) ExecScript(ctx context.Context, script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	ex, err := db.execerOf(ctx)
	if err != nil {
		return err
	}
	if _, err := ex.ExecContext(ctx, script); err != nil {
		// 错误不带脚本正文（可能极长，定位信息由上层带文件名补上）——
		// 是 R6.1"错误信息带语句文本"的例外，见 R11.4。
		return db.fail("执行脚本", err)
	}
	return nil
}
