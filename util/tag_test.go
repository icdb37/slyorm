package util

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 标签切分与选项解析
// ---------------------------------------------------------------------------

// 按逗号切分：括号与引号内的逗号不切分。
func TestSplitOpts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "id", want: []string{"id"}},
		{in: "id,null", want: []string{"id", "null"}},
		{in: "id, null", want: []string{"id", "null"}},
		{in: "type=decimal(10,2),null", want: []string{"type=decimal(10,2)", "null"}},
		{in: "defv='a,b'", want: []string{"defv='a,b'"}},
		{in: "defv=it's,null", want: []string{"defv=it's", "null"}},
	}
	for _, tc := range cases {
		got := SplitOpts(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("SplitOpts(%q) = %v, 期望 %v", tc.in, got, tc.want)
		}
	}
}

// 选项名转小写；首段是列名；裸 flag 的值是空串。
func TestParseOpts(t *testing.T) {
	opts := ParseOpts(SplitOpts("id,NULL,op=eq"))
	if opts[OptCol] != "id" {
		t.Errorf("列名 = %q, 期望 id", opts[OptCol])
	}
	if v, ok := opts["null"]; !ok || v != "" {
		t.Errorf("null 选项 = %q(%v), 期望空串", v, ok)
	}
	if opts["op"] != "eq" {
		t.Errorf("op 选项 = %q, 期望 eq", opts["op"])
	}
}

// 重复选项：首段与 col= 归一到同一个键，报重复。
func TestDuplicateOpt(t *testing.T) {
	if got := DuplicateOpt(SplitOpts("id,col=other")); got != OptCol {
		t.Errorf("重复选项 = %q, 期望 %q", got, OptCol)
	}
	if got := DuplicateOpt(SplitOpts("id,op=eq,op=like")); got != "op" {
		t.Errorf("重复选项 = %q, 期望 op", got)
	}
	if got := DuplicateOpt(SplitOpts("id,null")); got != "" {
		t.Errorf("没有重复时应返回空串, 实际 %q", got)
	}
}

// 脱外层引号：成对的引号才是分组标记。
func TestUnquoteValue(t *testing.T) {
	cases := map[string]string{
		"'hello world'": "hello world",
		"'it''s'":       "it's",
		`"abc"`:         "abc",
		"it's":          "it's",
		"'abc":          "'abc",
	}
	for in, want := range cases {
		if got := UnquoteValue(in); got != want {
			t.Errorf("UnquoteValue(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 布尔选项：裸 flag 算 true；显式取值认 true/false/1/0/yes/no/on/off。
func TestParseBoolValue(t *testing.T) {
	if b, ok := ParseBoolValue(""); !ok || !b {
		t.Error("裸 flag 应为 true")
	}
	if b, ok := ParseBoolValue("false"); !ok || b {
		t.Error("false 应解析为 false")
	}
	if _, ok := ParseBoolValue("maybe"); ok {
		t.Error("非法取值应报错")
	}
}

// 字段名 → 列名。
func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"ID":         "id",
		"Name":       "name",
		"CreatedAt":  "created_at",
		"HTTPStatus": "http_status",
	}
	for in, want := range cases {
		if got := SnakeCase(in); got != want {
			t.Errorf("SnakeCase(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
