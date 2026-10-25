package dbwm

import (
	"fmt"
	"strings"
)

// writeCountHead 写 COUNT 语句的开头：SELECT COUNT(*) FROM 表。
func (r *renderer) writeCountHead(q *Query) error {
	table, err := r.quoteTable(q.Table)
	if err != nil {
		return err
	}
	r.write("SELECT COUNT(*) FROM ")
	r.write(table)
	return nil
}

// writeSelectHead 写 SELECT 语句的开头：列清单（含聚合列）+ FROM 表。
func (r *renderer) writeSelectHead(q *Query) error {
	cols, err := r.quoteCols(q.Columns, q.Aggs...)
	if err != nil {
		return err
	}
	table, err := r.quoteTable(q.Table)
	if err != nil {
		return err
	}
	r.write("SELECT ")
	r.write(cols)
	r.write(" FROM ")
	r.write(table)
	return nil
}

// writeGroupBy 写 GROUP BY 子句；没有分组列时不拼。
func (r *renderer) writeGroupBy(q *Query) {
	if len(q.GroupBy) == 0 {
		return
	}
	parts := make([]string, len(q.GroupBy))
	for i, g := range q.GroupBy {
		parts[i] = r.quoteIdent(g)
	}
	r.write(" GROUP BY ")
	r.write(strings.Join(parts, ", "))
}

// writeHaving 写 HAVING 子句。
func (r *renderer) writeHaving(q *Query, alias aggAliases) error {
	if q.Having == nil {
		return nil
	}
	frag, args, err := r.renderGroup(q.Having, true, r.havingRef(q, alias))
	if err != nil {
		return err
	}
	if frag == "" {
		return nil
	}
	r.write(" HAVING ")
	r.bind(frag, args)
	return nil
}

// writeOrderBy 写 ORDER BY 子句：多列按给定顺序（-id 倒序、+id/id 正序）。
func (r *renderer) writeOrderBy(q *Query) error {
	if len(q.Sorts) == 0 {
		return nil
	}
	parts := make([]string, len(q.Sorts))
	for i, s := range q.Sorts {
		col, desc := parseSort(s)
		p := r.quoteIdent(col)
		if desc {
			p += " DESC"
		}
		parts[i] = p
	}
	r.write(" ORDER BY ")
	r.write(strings.Join(parts, ", "))
	return nil
}

// writeLimit 写分页子句（LIMIT/OFFSET）：offset 由页码与每页条数推导，
// 每页条数为 0（不限行数）时不渲染。
func (r *renderer) writeLimit(p Paging) error {
	if p.Size <= 0 {
		return nil
	}
	lim := r.d.Limit(p.Size, (p.Page-1)*p.Size)
	if lim == "" {
		return fmt.Errorf("dbwm: %s 方言不支持分页", r.d.Name())
	}
	r.write(" ")
	r.write(lim)
	return nil
}

// writeSelect 写完整的 SELECT 语句：
// 开头 → WHERE → GROUP BY → HAVING → ORDER BY → 分页。
func (r *renderer) writeSelect(pq *PagedQuery, alias aggAliases) error {
	q := &pq.Query
	if err := r.writeSelectHead(q); err != nil {
		return err
	}
	if err := r.writeWhere(q); err != nil {
		return err
	}
	r.writeGroupBy(q)
	if err := r.writeHaving(q, alias); err != nil {
		return err
	}
	if err := r.writeOrderBy(q); err != nil {
		return err
	}
	return r.writeLimit(pq.Paging)
}

// writeSelectInner 写 SELECT 的内层：开头 → WHERE → GROUP BY → HAVING ——
// 不带 ORDER BY 与分页。
//
// 分组总数查询的内层子查询用它：聚合要在内层才看得见，而 ORDER BY 与分页
// 不改行数，带进去只会浪费（分页带进去还会把总数数成页大小）。
func (r *renderer) writeSelectInner(q *Query, alias aggAliases) error {
	if err := r.writeSelectHead(q); err != nil {
		return err
	}
	if err := r.writeWhere(q); err != nil {
		return err
	}
	r.writeGroupBy(q)
	return r.writeHaving(q, alias)
}
