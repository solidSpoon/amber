package mcpserver

// stop_test.go：收尾工具的结论字段。
//
// 结论是读者第一眼看的东西，而它只有三个合法取值。以前它是个自由字符串：
// 写成 "passed" 也能存进库，界面只认 pass/fail/inconclusive，于是徽章静默消失。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeVerdict(t *testing.T) {
	ok := map[string]string{
		"":              "",
		"pass":          "pass",
		"FAIL":          "fail",
		" Inconclusive": "inconclusive",
	}
	for raw, want := range ok {
		got, err := normalizeVerdict(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeVerdict(%q) = %q, %v；期望 %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"passed", "ok", "true", "通过"} {
		if _, err := normalizeVerdict(bad); err == nil {
			t.Fatalf("%q 应该被拒（界面不认这个值，存进去等于丢结论）", bad)
		} else if !strings.Contains(err.Error(), "pass") {
			t.Fatalf("报错要说清可选值：%v", err)
		}
	}
}

// schema 里 verdict 必须是枚举，调用方一眼就能看到合法取值。
func TestStopSchemaEnumeratesVerdict(t *testing.T) {
	raw, err := json.Marshal(stopInputSchema())
	if err != nil {
		t.Fatalf("序列化 schema 失败: %v", err)
	}
	var parsed struct {
		Properties struct {
			Verdict struct {
				Enum []string `json:"enum"`
			} `json:"verdict"`
			Summary struct {
				Type string `json:"type"`
			} `json:"summary"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析 schema 失败: %v", err)
	}
	if len(parsed.Properties.Verdict.Enum) != len(verdictValues) {
		t.Fatalf("verdict 枚举 = %v，期望 %v", parsed.Properties.Verdict.Enum, verdictValues)
	}
	for i, value := range verdictValues {
		if parsed.Properties.Verdict.Enum[i] != value {
			t.Fatalf("verdict 枚举第 %d 项 = %q，期望 %q", i, parsed.Properties.Verdict.Enum[i], value)
		}
	}
	// 只有 sessionId 是必填：结论可以不填
	if len(parsed.Required) != 1 || parsed.Required[0] != "sessionId" {
		t.Fatalf("必填字段 = %v，期望只有 sessionId", parsed.Required)
	}
}
