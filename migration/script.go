package migration

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 脚本装载
// ---------------------------------------------------------------------------

// script 一个迁移脚本：版本号、序号、名称、文件路径与正文。
type script struct {
	version  int
	sequence int
	name     string
	file     string
	body     string
}

// after 判断脚本是否排在水位（版本号, 序号）之后：先比版本号、再比序号（R4.2）。
func (sc *script) after(version, sequence int) bool {
	if sc.version != version {
		return sc.version > version
	}
	return sc.sequence > sequence
}

// load 递归读取脚本目录，按（版本号, 序号）升序返回（R2）。
// 非 .sql 文件忽略；任一脚本名不合法或编号重复 → 报错（调用方因此零副作用，
// R2.5）。
func load(fsys fs.FS) ([]*script, error) {
	var out []*script
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("migration: 遍历脚本目录失败（%s）: %w", p, err)
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".sql") {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("migration: 读取脚本失败（%s）: %w", p, err)
		}
		sc, err := parseScript(p, string(body))
		if err != nil {
			return err
		}
		out = append(out, sc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b *script) int {
		if a.version != b.version {
			return a.version - b.version
		}
		return a.sequence - b.sequence
	})
	// R2.4 编号查重：排序后相邻比较，报错带两个冲突文件的路径。
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version && out[i].sequence == out[i-1].sequence {
			return nil, fmt.Errorf("migration: 脚本编号重复（%d_%d）：%s 与 %s",
				out[i].version, out[i].sequence, out[i-1].file, out[i].file)
		}
	}
	return out, nil
}

// parseScript 解析脚本文件名 {版本号}_{序号}_{名称}.sql，如 1000_001_seed_admin.sql
// （R2.1）：版本号 ≥1、序号 ≥0，数字段只认十进制数字字符（前导零按值解析，
// 与同值编号由 load 的查重拦下）；名称段可含下划线（取第三个 _ 之后的全部）。
func parseScript(file, body string) (*script, error) {
	base := strings.TrimSuffix(path.Base(file), ".sql")
	parts := strings.SplitN(base, "_", 3)
	if len(parts) != 3 || parts[2] == "" {
		return nil, fmt.Errorf("migration: 脚本文件名须为 {版本号}_{序号}_{名称}.sql: %s", file)
	}
	version, ok := parseNum(parts[0])
	if !ok || version < 1 {
		return nil, fmt.Errorf("migration: 脚本版本号须为 ≥1 的十进制数字: %s", file)
	}
	sequence, ok := parseNum(parts[1])
	if !ok || sequence < 0 {
		return nil, fmt.Errorf("migration: 脚本序号须为 ≥0 的十进制数字: %s", file)
	}
	return &script{version: version, sequence: sequence, name: parts[2], file: file, body: body}, nil
}

// parseNum 解析纯十进制数字段（只认数字字符，杜绝 +5 / 1e3 / 空格混进来）；
// ok=false 表示不是纯数字段。不造错误：定位信息（文件路径）由调用方带上。
func parseNum(s string) (n int, ok bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
