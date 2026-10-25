package slyorm

import (
	"context"

	"slyorm/insert"
)

// ---------------------------------------------------------------------------
// 审计列（当前操作用户）
//
// created_by / updated_by 之类审计列由门面统一填充：模型写
// `dbx:"created_by,deff=curuser"`，取值函数由业务装配期注入（见 prd-slyorm R12）。
// ---------------------------------------------------------------------------

// UserFn 从 ctx 取当前操作用户的函数形态。
//
// slyorm 不认识业务的上下文类型（如 csx.Context）—— 断言逻辑由业务注入；
// 返回值必须可赋给目标字段（int64 用户 ID、string 账号名…）。
type UserFn func(ctx context.Context) (any, error)

// RegisterUser 注入"从 ctx 取当前操作用户"的函数，门面把它注册成默认值函数
// **curuser**（走 insert 的默认值注册表，与 now 同一张表）。
//
// 注册语义（见 prd-slyorm R12.2）：
//   - 重复注册 → panic（注册表纪律）；
//   - 一个函数服务一种返回类型：int64 用户 ID 与 string 账号名是两种字段，
//     后者用 insert.RegisterDefault("curusername", fn2) 注册第二个函数名；
//   - 函数存在性与类型匹配在取值时校验（未注册或类型不匹配 → Create / Update
//     报错并指出字段与函数名），因此只需早于第一次写入。
func RegisterUser(fn UserFn) {
	if fn == nil {
		panic("slyorm: RegisterUser 的取用户函数不能为 nil")
	}
	insert.RegisterDefault("curuser", insert.Defaulter(fn))
}
