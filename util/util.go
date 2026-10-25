// Package util 是写规则与读规则共用的底层工具：标签文本处理、
// 值与零值判定、区间类型。
//
// 这里只放**两边都要用**的东西，且**不含"选项是什么意思"的语义** ——
// 语义解释仍由各包自己来：
//
//   - 写规则见 insert（docs/prd-insert.md）
//   - 读规则见 query（docs/prd-select.md）
//
// SQL 方言在 dbwm（dbwm/dialect.go）—— util 不含数据库概念。
//
// 标签文本的切分与解析只有这一份实现：读写两套规则共用一个标签键 dbx，
// 切分规则必须一致，否则同一个标签在两边会被拆成不同的样子。
//
// 文件分工：
//
//	util.go     本文件 —— 包注释与分工说明
//	tag.go      dbx 标签的文本处理（切分、选项解析、去引号、snake_case）
//	value.go    取值、零值判定、单列类型识别
//	range.go    区间类型 Range[T]
package util
