package insert

import (
	"context"
	"fmt"
	"reflect"

	"slyorm/dbwm"
	"slyorm/util"
)

// ---------------------------------------------------------------------------
// 写入计划（值成形）
// ---------------------------------------------------------------------------

// plan 是一次写入的列与参数。
type plan struct {
	columns []string
	values  []any
	autoPK  *Column // 自增主键列；nil 表示本次显式赋值或没有自增列
}

// insertIR 把计划组装成单行插入的 IR（表名按表名机制填好）。
func (p *plan) insertIR(m *Model) *dbwm.Insert {
	ir := &dbwm.Insert{
		Table:   m.TableRef(),
		Columns: p.columns,
		Rows:    [][]any{p.values},
	}
	if p.autoPK != nil {
		ir.AutoPKColumn = p.autoPK.Name
	}
	return ir
}

// applyDefaults 零值时补默认值并写回字段。
//
// deff（默认值函数）优先于 defv（字面量默认值）：函数能取到当前时间这类
// 依赖运行时的值，优先级更高。**defv 只在 INSERT 计划里启用**（R5.6 ——
// UPDATE 不取静态默认值，见 prd-slyorm R8）。
func applyDefaults(ctx context.Context, c *Column, rv reflect.Value, withDefv bool) error {
	if !util.IsZero(rv) || !rv.CanSet() {
		return nil
	}
	if fn := c.Opt(keyDeff); fn != "" {
		f, ok := lookupDefault(fn)
		if !ok {
			return fmt.Errorf("insert: 未注册的默认值函数 %q（字段 %s）", fn, c.GoName)
		}
		v, err := f(ctx)
		if err != nil {
			return fmt.Errorf("insert: 默认值函数 %q 执行失败: %w", fn, err)
		}
		if err := setValue(rv, v); err != nil {
			return fmt.Errorf("insert: 字段 %s: %w", c.GoName, err)
		}
		return nil
	}
	if withDefv && c.Has(keyDefv) {
		if err := setValue(rv, c.DefaultValue); err != nil {
			return fmt.Errorf("insert: 字段 %s: %w", c.GoName, err)
		}
	}
	return nil
}

// buildPlan 计算一次插入的列与参数。
//
// 除了自增列，列清单完全由模型决定：每个字段都参与写入 —— 零值写 Go 零值，
// 标了 null 的写 SQL NULL。因此批量插入时各行列清单天然对齐（见 prd-insert R4.1）。
func buildPlan(ctx context.Context, model *Model, elem reflect.Value) (*plan, error) {
	p := &plan{}
	for i := range model.Columns {
		c := &model.Columns[i]
		rv := fieldAt(elem, c)

		// 自增主键（autopk）：值为零时不参与 INSERT，交给数据库生成，成功后回填；
		// 调用方给了显式值就按显式值写入，不回填。
		if c.IsAutoPK() && util.IsZero(rv) {
			p.autoPK = c
			continue
		}

		// 默认值只在零值时补；补完值不再零，因此默认值优先于 null。
		// 路径上有 nil 指针时定位不到字段，整组按零值写（见 prd-insert R3 ⑥）。
		if err := applyDefaults(ctx, c, rv, true); err != nil {
			return nil, err
		}
		val, err := bindValue(c, rv)
		if err != nil {
			return nil, err
		}
		p.columns = append(p.columns, c.Name)
		p.values = append(p.values, val)
	}
	if len(p.columns) == 0 {
		return nil, fmt.Errorf("insert: %s 没有可写入的列", model.Table)
	}
	return p, nil
}

// buildUpdatePlan 计算一次更新的 SET 列与值。
//
// 与插入同一套值成形；差异：deff 生效（审计列刷新）、defv 不生效，
// 自增列照常进 SET —— 主键剔除由门面按表模型的主键集合做（prd-slyorm R8.3）。
func buildUpdatePlan(ctx context.Context, model *Model, elem reflect.Value) (*plan, error) {
	p := &plan{}
	for i := range model.Columns {
		c := &model.Columns[i]
		rv := fieldAt(elem, c)
		if err := applyDefaults(ctx, c, rv, false); err != nil {
			return nil, err
		}
		val, err := bindValue(c, rv)
		if err != nil {
			return nil, err
		}
		p.columns = append(p.columns, c.Name)
		p.values = append(p.values, val)
	}
	if len(p.columns) == 0 {
		return nil, fmt.Errorf("insert: %s 没有可更新的列", model.Table)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// 批量计划与语句渲染
// ---------------------------------------------------------------------------

// buildInsertAll 逐行计算插入计划并合成一个 IR（Table 留空由调用方填）。
//
// 各行的列清单必须一致 —— 唯一会随行变化的只有自增列：
// 要么整批都是零值（由数据库生成），要么整批都显式赋值，混用直接报错。
func buildInsertAll(ctx context.Context, rv reflect.Value) (*dbwm.Insert, []reflect.Value, error) {
	var (
		m        *Model
		elemType reflect.Type
		columns  []string
		skipPK   bool
		plans    []*plan
		items    []reflect.Value // 解引用后的各行模型值
	)
	for i := 0; i < rv.Len(); i++ {
		item, err := structAt(rv, i)
		if err != nil {
			return nil, nil, err
		}
		if m == nil {
			if m, err = writeModelOf(item.Type()); err != nil {
				return nil, nil, err
			}
			elemType = item.Type()
		} else if item.Type() != elemType {
			return nil, nil, fmt.Errorf("insert: InsertAll 元素类型不一致：%s 与 %s", elemType, item.Type())
		}
		p, err := buildPlan(ctx, m, item)
		if err != nil {
			return nil, nil, err
		}
		if i == 0 {
			columns = p.columns
			skipPK = p.autoPK != nil
		} else if (p.autoPK != nil) != skipPK {
			return nil, nil, fmt.Errorf(
				"insert: InsertAll 第 %d 项的自增列与首行不一致：自增列要么整批都是零值（交给数据库生成），要么整批都显式赋值", i)
		}
		plans = append(plans, p)
		items = append(items, item)
	}
	ir := &dbwm.Insert{Columns: columns}
	for _, p := range plans {
		ir.Rows = append(ir.Rows, p.values)
	}
	if skipPK {
		ir.AutoPKColumn = m.AutoPKColumn()
	}
	return ir, items, nil
}
