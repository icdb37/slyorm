package slyorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"slyorm/dbwm"
	"slyorm/insert"
	"slyorm/model"
	rows "slyorm/select" // 包名 rows：select 是 Go 关键字，不能作包名
)

// ---------------------------------------------------------------------------
// Collection：表访问器
// ---------------------------------------------------------------------------

// Collection 绑定一张表的访问器：表模型 M 与表名在构造期确定，
// 之后除校验缓存外只读、并发安全。方法各自收投影类型 T —— 列集合与
// 主键看 M，读写形态看 T（见 prd-slyorm R1/R2）。
//
// 类型本身不带类型参数 —— M 的信息在构造期固化为列集合与主键标记；
// 泛型只落在构造期（NewCollection[M]）与方法级（投影 T、条件 Q）。
type Collection struct {
	wm    *dbwm.DB
	table string
	m     *model.Model
	// readOK 已通过列集合校验的投影类型（T 解指针后的类型为键）：
	// 校验成功才有下一次免检，失败不缓存（每次调用都报同一错误，见 R13.3）。
	readOK sync.Map // reflect.Type -> struct{}
}

// NewCollection 按表模型 M 与表名构造表访问器。
//
// 表名显式必填（支持 "schema.table" 带前缀），与模型解耦 —— 同一模型可映射
// 多张表；M 可以是指针（按解指针后的类型解析与缓存）。构造期解析 M 的
// 标签（静态列集合 + 主键标记）并过一遍**写规则校验**（选项取值、语义冲突、
// defv 字面量），解析失败在此报错，不留到首次读写（R1.5 / R13.2）。
func NewCollection[M any](db *DB, table string) (*Collection, error) {
	if db == nil || db.wm == nil {
		return nil, fmt.Errorf("slyorm: 句柄不能为 nil")
	}
	if err := checkTableName(table); err != nil {
		return nil, err
	}
	m, err := model.Parse(new(M))
	if err != nil {
		return nil, fmt.Errorf("slyorm: 表模型解析失败: %w", err)
	}
	if err := insert.ValidateModel(reflect.TypeOf(new(M))); err != nil {
		return nil, fmt.Errorf("slyorm: 表模型 %s 的写规则校验失败: %w", m.Type, err)
	}
	return &Collection{wm: db.wm, table: table, m: m}, nil
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

// Get 单行查询：Q 按读规则解析为条件，T 是投影类型；未命中返回 (nil, nil)，
// 多于一行取第一行。条件取值后为空 → 报错（空条件取回的是表头行，几乎必然
// 是 bug，见 prd-slyorm R3）。
func (c *Collection) Get[Q any, T any](ctx context.Context, q *Q) (*T, error) {
	return c.get[T](ctx, q)
}

// Xget 是 Get 的动态分派版：q 以 any 传入，按 q 的运行时类型解析 dbx 标签 ——
// 指定 T 时不必连带写出可从 q 推断的 Q（Go 的类型参数按前缀补齐）。
func (c *Collection) Xget[T any](ctx context.Context, q any) (*T, error) {
	return c.get[T](ctx, q)
}

// Query 多行查询：条件取值后为空 = 查全表（合法）；没有命中返回空切片。
func (c *Collection) Query[Q any, T any](ctx context.Context, q *Q) ([]*T, error) {
	return c.query[T](ctx, q)
}

// Xqry 是 Query 的动态分派版（q 以 any 传入）。
func (c *Collection) Xqry[T any](ctx context.Context, q any) ([]*T, error) {
	return c.query[T](ctx, q)
}

// List 分页列表：过滤来自 req.Query，排序与翻页来自 req 的分页参数，
// 返回命中总行数与当页数据（见 prd-slyorm R5）。req 为 nil 等价零值请求。
func (c *Collection) List[Q any, T any](ctx context.Context, req *ListRequest[Q]) (*ListResponse[T], error) {
	if req == nil {
		req = &ListRequest[Q]{}
	}
	if err := c.checkRead[T](); err != nil {
		return nil, err
	}
	w, err := c.where(ctx, req.Query)
	if err != nil {
		return nil, err
	}
	for _, s := range req.Sort {
		name := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "-"), "+")
		if !c.m.HasColumn(name) {
			return nil, fmt.Errorf("slyorm: 排序列 %q 不在表 %s 的列集合", name, c.table)
		}
	}
	page, size := req.Page, req.Size
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size, page = 0, 1 // 不分页取全量：页码无从谈起，强制 1（dbwm 对 Size==0&&Page>1 报错）
	}
	total, data, err := rows.QueryPaged[T](ctx, c.wm, &dbwm.PagedQuery{
		Query:  dbwm.Query{Table: c.table, Where: w, Sorts: req.Sort},
		Paging: dbwm.Paging{Page: page, Size: size},
	})
	if err != nil {
		return nil, err
	}
	return &ListResponse[T]{Total: total, Data: data}, nil
}

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

// Create 插入一行：T 按写规则解析（复用 insert 的 IR 构造入口），表名用
// Collection 绑定的表名。自增列零值交给数据库生成并回填，默认值写回结构体。
func (c *Collection) Create[T any](ctx context.Context, p *T) error {
	if err := checkProj[T](); err != nil {
		return err
	}
	ir, err := insert.BuildInsertIR(ctx, p)
	if err != nil {
		return err
	}
	if err := c.checkWrite[T](ir.Columns); err != nil {
		return err
	}
	ir.Table = c.table
	res, err := c.wm.Insert(ctx, ir)
	if err != nil {
		return err
	}
	if len(res.IDs) == 0 {
		return nil
	}
	return insert.FillAutoID(p, res.IDs[0])
}

// Creates 批量插入：与 Create 同一张表；多 VALUES、超参数上限由 dbwm
// 自动分块，**分块不自动包事务**（要么包 db.Tx，要么接受部分成功）。
// 自增列要么整批零值（由数据库生成并按行序回填），要么整批显式赋值。
func (c *Collection) Creates[T any](ctx context.Context, ps []T) error {
	if len(ps) == 0 {
		return nil
	}
	if err := checkProj[T](); err != nil {
		return err
	}
	ir, err := insert.BuildInsertAllIR(ctx, ps)
	if err != nil {
		return err
	}
	if err := c.checkWrite[T](ir.Columns); err != nil {
		return err
	}
	ir.Table = c.table
	res, err := c.wm.Insert(ctx, ir)
	if err != nil {
		return err
	}
	for i := range ps {
		if i >= len(res.IDs) {
			break
		}
		if err := insert.FillAutoID(&ps[i], res.IDs[i]); err != nil {
			return err
		}
	}
	return nil
}

// Update 按条件更新：SET 列 = 投影 T 的全部列（零值也写），M 的主键列剔除
// （行身份由 WHERE 定位）；条件取值后为空 → 报错。
// 值成形与插入同一套，deff 生效（审计列刷新）、defv 不生效。
func (c *Collection) Update[Q any, T any](ctx context.Context, q *Q, p *T) error {
	if err := checkProj[T](); err != nil {
		return err
	}
	w, err := c.where(ctx, q)
	if err != nil {
		return err
	}
	if w == nil {
		return c.errNoCond("Update")
	}
	up, err := insert.BuildUpdateIR(ctx, p)
	if err != nil {
		return err
	}
	if err := c.checkWrite[T](up.Columns); err != nil {
		return err
	}
	up.Columns, up.Values = c.dropPK(up.Columns, up.Values)
	if len(up.Columns) == 0 {
		return fmt.Errorf("slyorm: 无可更新字段（投影全部为主键列）")
	}
	up.Table = c.table
	up.Where = w
	_, err = c.wm.Update(ctx, up)
	return err
}

// Delete 按条件删除：条件取值后为空 → 报错（拒绝全表删除）。
func (c *Collection) Delete[Q any](ctx context.Context, q *Q) error {
	w, err := c.where(ctx, q)
	if err != nil {
		return err
	}
	if w == nil {
		return c.errNoCond("Delete")
	}
	_, err = c.wm.Delete(ctx, &dbwm.Delete{Table: c.table, Where: w})
	return err
}
