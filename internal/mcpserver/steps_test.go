package mcpserver

// steps_test.go：run 的步骤契约。
//
// 这几条测试盯的是"调用方能不能只看 schema 就写对"：
// 动作是枚举、每个动作要什么字段说清楚、写错当场报一句人话。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActionEnumMatchesActionTable(t *testing.T) {
	raw, err := json.Marshal(runInputSchema())
	if err != nil {
		t.Fatalf("序列化 schema 失败: %v", err)
	}
	var parsed struct {
		Properties struct {
			Steps struct {
				Items struct {
					Properties struct {
						Action struct {
							Enum []string `json:"enum"`
						} `json:"action"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"steps"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析 schema 失败: %v", err)
	}
	enum := parsed.Properties.Steps.Items.Properties.Action.Enum
	if len(enum) != len(actionTable) {
		t.Fatalf("action 的枚举 = %v，期望 %d 项", enum, len(actionTable))
	}
	for i, action := range actionTable {
		if enum[i] != action.Name {
			t.Fatalf("action 枚举第 %d 项 = %q，期望 %q", i, enum[i], action.Name)
		}
	}
	// 历史上被删掉的动作不能再出现
	for _, gone := range []string{"assert", "snapshot", "wait_and_capture"} {
		for _, name := range enum {
			if name == gone {
				t.Fatalf("枚举里不该再有 %q（它要么被删了、要么只是 capture 的别名）", gone)
			}
		}
	}
}

func TestValidateStep(t *testing.T) {
	cases := []struct {
		name string
		step batchStep
		want string // 期望错误里出现的关键字；空表示应该通过
	}{
		{"click 有 ref", batchStep{Action: "click", Ref: "e3"}, ""},
		{"click 只有坐标", batchStep{Action: "click", X: 10, Y: 20}, ""},
		{"click 什么都没有", batchStep{Action: "click"}, "ref"},
		{"hover 用文字", batchStep{Action: "hover", Match: "Skip"}, ""},
		{"fill 缺 text", batchStep{Action: "fill", Ref: "e1"}, "text"},
		{"fill 齐全", batchStep{Action: "fill", Ref: "e1", Text: "hi"}, ""},
		{"press 缺 key", batchStep{Action: "press"}, "key"},
		{"type 缺 text", batchStep{Action: "type"}, "text"},
		{"eval 缺 script", batchStep{Action: "eval"}, "script"},
		{"wait 缺 for", batchStep{Action: "wait"}, "sleep"},
		{"wait 给了 ms", batchStep{Action: "wait", For: "true", MS: 500}, "sleep"},
		{"sleep 正常", batchStep{Action: "sleep", MS: 300}, ""},
		{"sleep 给了 for", batchStep{Action: "sleep", MS: 300, For: "true"}, "wait"},
		{"sleep 超上限", batchStep{Action: "sleep", MS: 90000}, "60000"},
		{"capture 不给参数", batchStep{Action: "capture"}, ""},
		{"capture 给了 ms", batchStep{Action: "capture", MS: 100}, "sleep"},
		{"未知动作", batchStep{Action: "assert"}, "未知动作"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStep(tc.step)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("期望通过，却报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错（含 %q），却通过了", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 %q 里没有 %q（调用方看了也不知道该改哪儿）", err.Error(), tc.want)
			}
		})
	}
}

func TestActionHelpListsEveryAction(t *testing.T) {
	help := actionHelp()
	for _, action := range actionTable {
		if !strings.Contains(help, action.Name) {
			t.Fatalf("动作说明里漏了 %q：%s", action.Name, help)
		}
	}
}

func TestNormalizeActionAliases(t *testing.T) {
	for _, alias := range []string{"snapshot", "wait_and_capture"} {
		if got := normalizeAction(alias); got != "capture" {
			t.Fatalf("%q 应归一成 capture，得到 %q", alias, got)
		}
	}
	if got := normalizeAction("sleep"); got != "sleep" {
		t.Fatalf("非别名不该被改写，得到 %q", got)
	}
}

// 动作表本身要完整：三处（枚举、说明、自检）都从它长出来，缺一项就是调用方踩坑。
func TestActionTableIsComplete(t *testing.T) {
	for _, spec := range actionTable {
		if spec.Name == "" || spec.Need == "" || spec.Note == "" {
			t.Fatalf("动作 %+v 缺少名字/必填字段/说明", spec)
		}
		if spec.Run == nil {
			t.Fatalf("动作 %q 没有执行体", spec.Name)
		}
		if got, ok := lookupAction(spec.Name); !ok || got.Name != spec.Name {
			t.Fatalf("动作 %q 查不回来", spec.Name)
		}
	}
	// 说明里每个动作都要出现，且带上"干什么"
	lines := actionLines()
	for _, spec := range actionTable {
		if !strings.Contains(lines, spec.Note) {
			t.Fatalf("动作说明里漏了 %q 的用途：%s", spec.Name, lines)
		}
	}
}
