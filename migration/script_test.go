package migration

import (
	"strings"
	"testing"
	"testing/fstest"
)

// ---------------------------------------------------------------------------
// R2 脚本装载与命名
// ---------------------------------------------------------------------------

// mapfs 由 map 构造脚本目录替身。
func mapfs(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range files {
		out[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return out
}

func TestParseScript(t *testing.T) {
	sc, err := parseScript("v1.0/1000_001_seed_admin.sql", "SELECT 1;")
	if err != nil {
		t.Fatal(err)
	}
	// 名称段可含下划线：取第三个 _ 之后的全部（R2.1）。
	if sc.version != 1000 || sc.sequence != 1 || sc.name != "seed_admin" || sc.file != "v1.0/1000_001_seed_admin.sql" {
		t.Fatalf("解析结果 = %d/%d/%s，期望 1000/1/seed_admin", sc.version, sc.sequence, sc.name)
	}
	if sc.body != "SELECT 1;" {
		t.Fatalf("正文 = %q", sc.body)
	}
}

func TestParseScriptInvalid(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"v1.0/1000_001.sql", "须为"},        // 段数不足
		{"v1.0/1000_001_.sql", "须为"},       // 名称段为空
		{"v1.0/xx_001_name.sql", "版本号"},   // 非数字
		{"v1.0/0_001_name.sql", "版本号"},    // 版本号 0
		{"v1.0/+5_001_name.sql", "版本号"},   // 符号
		{"v1.0/1000_-1_name.sql", "序号"},  // 负号
		{"v1.0/1000_1e2_name.sql", "序号"}, // 指数形式
	}
	for _, c := range cases {
		if _, err := parseScript(c.file, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("parseScript(%q) 错误 = %v，期望含 %q", c.file, err, c.want)
		}
	}
}

func TestLoadSortsAndIgnores(t *testing.T) {
	fsys := mapfs(map[string]string{
		"v1.0/1000_002_b.sql": "",
		"v1.1/1100_001_c.sql": "",
		"v1.0/1000_000_a.sql": "",
		"v1.0/readme.txt":     "忽略非 sql 文件",
	})
	scripts, err := load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 3 {
		t.Fatalf("装载 %d 个脚本（非 .sql 应被忽略），期望 3", len(scripts))
	}
	want := []string{"v1.0/1000_000_a.sql", "v1.0/1000_002_b.sql", "v1.1/1100_001_c.sql"}
	for i, sc := range scripts {
		if sc.file != want[i] {
			t.Fatalf("第 %d 个是 %s，期望 %s（按版本号、序号升序）", i, sc.file, want[i])
		}
	}
}

func TestLoadDuplicateNumber(t *testing.T) {
	// 前导零按值解析：007 与 7 是同号，查重要拦下（R2.1 / R2.4）。
	fsys := mapfs(map[string]string{
		"v1.0/1000_007_a.sql": "",
		"v1.0/1000_7_b.sql":   "",
	})
	_, err := load(fsys)
	if err == nil || !strings.Contains(err.Error(), "重复") ||
		!strings.Contains(err.Error(), "v1.0/1000_007_a.sql") ||
		!strings.Contains(err.Error(), "v1.0/1000_7_b.sql") {
		t.Fatalf("错误 = %v，期望报编号重复且带两个冲突文件", err)
	}
}

func TestAfterWatermark(t *testing.T) {
	sc := &script{version: 1000, sequence: 5}
	cases := []struct {
		v, s int
		want bool
	}{
		{999, 9, true},   // 版本号更大 → 执行
		{1000, 4, true},  // 同版本序号更大 → 执行
		{1000, 5, false}, // 等于水位 → 跳过
		{1000, 6, false}, // 序号更小 → 跳过
		{1001, 0, false}, // 版本号更小 → 跳过
	}
	for _, c := range cases {
		if got := sc.after(c.v, c.s); got != c.want {
			t.Errorf("after(%d, %d) = %v，期望 %v", c.v, c.s, got, c.want)
		}
	}
}
