package dbwm

import (
	"fmt"
	"slices"
	"strings"
)

// ---------------------------------------------------------------------------
// 语句渲染
// ---------------------------------------------------------------------------

// subqueryAlias 是分组总数查询里子查询的别名。
const subqueryAlias = "dbwm_sub"

// renderer 组装一条语句：SQL 文本 + 绑定参数。
//
// 占位符由渲染器统一分配：算子与写入路径调用 nextPlaceholder 拿到方言的
// 占位符（PostgreSQL 的 $1、$2…），编号在整条语句里连续。
//
// 分配顺序必须与占位符在文本里的出现顺序一致 —— 由渲染顺序保证：
// SELECT 是 WHERE → HAVING，UPDATE 是 SET → WHERE，INSERT 是按行按列。
//
// 渲染器是**单条语句私有**的：一次渲染一个实例，不跨 goroutine 共享。
type renderer struct {
	d Dialect
	// q 是方言的引用字符（构造时取一次），标识符引用都用它 —— 不再逐个
	// 走接口调用。转义（doubling）与包级 QuoteIdent 同一份规则。
	q    string
	sb   strings.Builder
	args []any
	// idxPlaceholder 已分配的占位符数，同时是最后一个占位符的编号。
	idxPlaceholder int
}

// newRenderer 建一个渲染器。
func newRenderer(d Dialect) *renderer {
	d = OrDefaultDialect(d)
	return &renderer{d: d, q: d.QuoteChar()}
}

// nextPlaceholder 分配下一个占位符：编号在整条语句里连续。
func (r *renderer) nextPlaceholder() string {
	r.idxPlaceholder++
	return r.d.Placeholder(r.idxPlaceholder)
}

// quoteIdent 给标识符加引号：引用字符在构造时取好（r.q），转义在这里做。
func (r *renderer) quoteIdent(ident string) string {
	q := r.q
	return q + strings.ReplaceAll(ident, q, q+q) + q
}

// write 追加纯文本（不含绑定参数）。
func (r *renderer) write(s string) { r.sb.WriteString(s) }

// bind 追加一个带绑定参数的片段。
//
// 片段里的占位符已由算子分配好，参数与占位符的个数对应在片段产生处校验
// （见 renderCond）；这里只把文本与参数接上。
func (r *renderer) bind(frag string, args []any) {
	r.sb.WriteString(frag)
	r.args = append(r.args, args...)
}

// build 完成语句：文本里已经是方言的占位符，只核对参数个数。
func (r *renderer) build() (string, []any, error) {
	if r.idxPlaceholder != len(r.args) {
		return "", nil, fmt.Errorf("dbwm: 语句里的占位符数（%d）与绑定参数数（%d）不一致",
			r.idxPlaceholder, len(r.args))
	}
	return r.sb.String(), r.args, nil
}

// quoteTable 引用表名：支持 schema.table 形式，按 . 分段逐段引用。
//
// 标识符不做本地校验 —— 方言的引用会转义引号（注入面封死），写错的表名
// 由数据库报错暴露（错误带语句文本，见 R6.1）。
func (r *renderer) quoteTable(table string) (string, error) {
	if table == "" {
		return "", fmt.Errorf("dbwm: 表名不能为空")
	}
	parts := strings.Split(table, ".")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = r.quoteIdent(p)
	}
	return strings.Join(out, "."), nil
}

// aggAliases 是聚合别名 → 聚合表达式：HAVING 里别名要改写成表达式。
type aggAliases map[string]string

// plan 校验查询描述并算出渲染需要的派生数据。
//
// 校验针对 q 的字段做 —— q 是唯一的信息源，派生数据只有聚合别名 → 表达式。
// 校验全在渲染期做 —— 构造期没有方言可依赖，IR 只当纯数据。
func (r *renderer) plan(q *Query) (aggAliases, error) {
	if q == nil {
		return nil, fmt.Errorf("dbwm: 查询描述不能为空")
	}
	if len(q.Columns) == 0 {
		return nil, fmt.Errorf("dbwm: 列清单不能为空（不支持 SELECT *：列序要与扫描地址对齐）")
	}
	for _, g := range q.GroupBy {
		if !slices.Contains(q.Columns, g) {
			return nil, fmt.Errorf("dbwm: 分组列 %q 必须出现在列清单里（结果集要先给出分组键）", g)
		}
	}
	if len(q.Aggs) > 0 && len(q.GroupBy) == 0 {
		return nil, fmt.Errorf("dbwm: 有聚合项但没有分组维度（GroupBy 为空）")
	}
	if q.Having != nil && len(q.Having.Items) > 0 && len(q.GroupBy) == 0 {
		return nil, fmt.Errorf("dbwm: HAVING 需要分组维度（GroupBy 为空）")
	}
	alias := aggAliases{}
	for _, a := range q.Aggs {
		if a == nil {
			return nil, fmt.Errorf("dbwm: 聚合项不能为空")
		}
		if slices.Contains(q.Columns, a.Alias) {
			return nil, fmt.Errorf("dbwm: 聚合别名 %q 与列清单里的列名重名", a.Alias)
		}
		if _, dup := alias[a.Alias]; dup {
			return nil, fmt.Errorf("dbwm: 聚合别名 %q 重复", a.Alias)
		}
		expr, err := r.aggExpr(a)
		if err != nil {
			return nil, err
		}
		alias[a.Alias] = expr
	}
	for _, s := range q.Sorts {
		col, _ := parseSort(s)
		if col == "" {
			return nil, fmt.Errorf("dbwm: 排序项 %q 没有列名（-id 倒序、+id 或 id 正序）", s)
		}
		if len(q.GroupBy) == 0 {
			continue
		}
		if slices.Contains(q.GroupBy, col) {
			continue
		}
		if _, ok := alias[col]; ok {
			continue
		}
		return nil, fmt.Errorf("dbwm: 分组查询只能按分组列或聚合别名排序，%q 不在其列", col)
	}
	return alias, nil
}

// validatePaging 校验分页参数：页码从 1 起；每页条数为 0（不限行数）时页码只能是 0 或 1。
func validatePaging(p Paging) error {
	switch {
	case p.Size < 0:
		return fmt.Errorf("dbwm: 每页条数不能为负（当前 %d）", p.Size)
	case p.Size > 0 && p.Page < 1:
		return fmt.Errorf("dbwm: 页码从 1 起（当前 %d）", p.Page)
	case p.Size == 0 && (p.Page < 0 || p.Page > 1):
		return fmt.Errorf("dbwm: 每页条数为 0（不限行数）时页码只能是 0 或 1（当前 %d）", p.Page)
	}
	return nil
}

// aggExpr 渲染一个聚合表达式：COUNT(*) / SUM("列")。
//
// 聚合列名不做本地校验 —— 由方言引用，写错由数据库报错暴露。
func (r *renderer) aggExpr(a *Agg) (string, error) {
	if a == nil {
		return "", fmt.Errorf("dbwm: 聚合项不能为空")
	}
	switch a.Func {
	case AggCount:
		if a.Column == "" {
			return "COUNT(*)", nil
		}
		return "COUNT(" + r.quoteIdent(a.Column) + ")", nil
	case AggSum, AggAvg, AggMin, AggMax:
		if a.Column == "" {
			return "", fmt.Errorf("dbwm: 聚合函数 %q 必须指定列名", a.Func)
		}
		return strings.ToUpper(string(a.Func)) + "(" + r.quoteIdent(a.Column) + ")", nil
	default:
		return "", fmt.Errorf("dbwm: 未知的聚合函数 %q", a.Func)
	}
}

// quoteCols 渲染列清单：普通列在前、聚合列在后（plan 已保证列清单非空）。
func (r *renderer) quoteCols(cols []string, aggs ...*Agg) (string, error) {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, r.quoteIdent(c))
	}
	out := strings.Join(parts, ",")
	for _, a := range aggs {
		expr, err := r.aggExpr(a)
		if err != nil {
			return "", err
		}
		out += ", " + expr + " AS " + r.quoteIdent(a.Alias)
	}
	return out, nil
}

// writeWhere 写 WHERE 子句；条件渲染后为空时不拼关键字。
func (r *renderer) writeWhere(q *Query) error {
	if q.Where == nil {
		return nil
	}
	frag, args, err := r.renderGroup(q.Where, true, r.whereRef())
	if err != nil {
		return err
	}
	if frag == "" {
		return nil
	}
	r.write(" WHERE ")
	r.bind(frag, args)
	return nil
}

// havingRef 解析 HAVING 里的一列：分组列按标识符引用，聚合别名改写成聚合表达式。
//
// 聚合别名改写成表达式，是因为 PostgreSQL 的 HAVING 不认输出别名 ——
// 改写后三库通吃。上层 IR 里始终只写别名。
func (r *renderer) havingRef(q *Query, alias aggAliases) columnRef {
	return func(column string) (string, error) {
		if expr, ok := alias[column]; ok {
			return expr, nil
		}
		if slices.Contains(q.GroupBy, column) {
			return r.quoteIdent(column), nil
		}
		return "", fmt.Errorf("dbwm: HAVING 里只能引用分组列或聚合别名，%q 不在其列", column)
	}
}

// parseSort 解析一个排序项：前缀 - 倒序、+ 或无前缀正序；
// 返回列名与是否倒序（如 "-id" → ("id", true)、"+id"/"id" → ("id", false)）。
func parseSort(s string) (column string, desc bool) {
	if strings.HasPrefix(s, "-") {
		return s[1:], true
	}
	if strings.HasPrefix(s, "+") {
		return s[1:], false
	}
	return s, false
}

// requiredWhere 渲染条件组并要求非空。
//
// 全表更新与全表删除是误操作的主要来源 —— 宁可报错也不放行。
func (r *renderer) requiredWhere(g *Wnode, what string) error {
	frag, args, err := r.renderGroup(g, true, r.whereRef())
	if err != nil {
		return err
	}
	if frag == "" {
		return fmt.Errorf("dbwm: %s 缺少 WHERE 条件（拒绝全表操作）", what)
	}
	r.write(" WHERE ")
	r.bind(frag, args)
	return nil
}

// ---------------------------------------------------------------------------
// 各语句的渲染入口
// ---------------------------------------------------------------------------

// renderQuery 渲染一条查询语句：分页照给（分页为零 = 不限行数）。
func renderQuery(d Dialect, q *PagedQuery) (string, []any, error) {
	if q == nil {
		return "", nil, fmt.Errorf("dbwm: 查询描述不能为空")
	}
	r := newRenderer(d)
	alias, err := r.plan(&q.Query)
	if err != nil {
		return "", nil, err
	}
	if err := validatePaging(q.Paging); err != nil {
		return "", nil, err
	}
	if err := r.writeSelect(q, alias); err != nil {
		return "", nil, err
	}
	return r.build()
}

// renderCount 渲染总数查询。
//
// 没有分组时是 `SELECT COUNT(*) FROM 表`；有分组时总数是**分组数** ——
// 内层子查询带上分组与 HAVING（保证聚合在内层可见），外层数子查询的行数。
func renderCount(d Dialect, q *PagedQuery) (string, []any, error) {
	if q == nil {
		return "", nil, fmt.Errorf("dbwm: 查询描述不能为空")
	}
	r := newRenderer(d)
	alias, err := r.plan(&q.Query)
	if err != nil {
		return "", nil, err
	}
	if err := validatePaging(q.Paging); err != nil {
		return "", nil, err
	}
	if len(q.GroupBy) == 0 {
		if err := r.writeCountHead(&q.Query); err != nil {
			return "", nil, err
		}
		if err := r.writeWhere(&q.Query); err != nil {
			return "", nil, err
		}
		return r.build()
	}
	r.write("SELECT COUNT(*) FROM (")
	if err := r.writeSelectInner(&q.Query, alias); err != nil {
		return "", nil, err
	}
	r.write(") ")
	r.write(r.quoteIdent(subqueryAlias))
	return r.build()
}

// renderInsert 渲染一条 INSERT 语句：rows 是本次执行的行。
func renderInsert(d Dialect, table string, columns []string, rows [][]any) (string, []any, error) {
	r := newRenderer(d)
	qt, err := r.quoteTable(table)
	if err != nil {
		return "", nil, err
	}
	cols, err := r.quoteCols(columns)
	if err != nil {
		return "", nil, err
	}
	r.write("INSERT INTO ")
	r.write(qt)
	r.write("(")
	r.write(cols)
	r.write(") VALUES ")

	var (
		values strings.Builder
		args   []any
	)
	for i, row := range rows {
		if i > 0 {
			values.WriteString(",")
		}
		values.WriteString("(")
		for j := range columns {
			if j > 0 {
				values.WriteString(",")
			}
			values.WriteString(r.nextPlaceholder())
		}
		values.WriteString(")")
		args = append(args, row...)
	}
	r.bind(values.String(), args)
	return r.build()
}

// RenderInsert 渲染一条插入语句（只生成 SQL 与参数，不执行）：
// 与执行路径（Insert）同一份渲染器，预览与执行的语句文本一致。
//
// 供需要预览 SQL 的调用方使用（如 insert 包的 BuildInsert）—— 语法归 dbwm，
// 调用方不自己拼 INSERT 文本（见 prd-slyorm §3 的切口）。校验同执行路径。
func RenderInsert(d Dialect, in *Insert) (string, []any, error) {
	if err := validateInsert(in); err != nil {
		return "", nil, err
	}
	return renderInsert(d, in.Table, in.Columns, in.Rows)
}

// renderUpdate 渲染一条 UPDATE 语句。
//
// 参数顺序：SET 的值在前，WHERE 的条件在后。
func renderUpdate(d Dialect, up *Update) (string, []any, error) {
	r := newRenderer(d)
	qt, err := r.quoteTable(up.Table)
	if err != nil {
		return "", nil, err
	}
	sets := make([]string, len(up.Columns))
	for i, c := range up.Columns {
		sets[i] = r.quoteIdent(c) + "=" + r.nextPlaceholder()
	}
	r.write("UPDATE ")
	r.write(qt)
	r.write(" SET ")
	r.bind(strings.Join(sets, ","), up.Values)
	if err := r.requiredWhere(up.Where, "UPDATE"); err != nil {
		return "", nil, err
	}
	return r.build()
}

// renderDelete 渲染一条 DELETE 语句。
func renderDelete(d Dialect, del *Delete) (string, []any, error) {
	r := newRenderer(d)
	qt, err := r.quoteTable(del.Table)
	if err != nil {
		return "", nil, err
	}
	r.write("DELETE FROM ")
	r.write(qt)
	if err := r.requiredWhere(del.Where, "DELETE"); err != nil {
		return "", nil, err
	}
	return r.build()
}

// stmtForErr 给出用于错误信息的语句文本：批量插入的语句可能非常长，截断。
func stmtForErr(stmt string) string {
	const max = 200
	if len(stmt) <= max {
		return stmt
	}
	return stmt[:max] + "…（截断）"
}

// execFail 构造执行失败的错误：带上语句文本，错误信息才能指出"哪条语句"（见 R6.1）。
func execFail(action, stmt string, err error) error {
	return fmt.Errorf("dbwm: %s失败: %w；语句: %s", action, err, stmtForErr(stmt))
}
