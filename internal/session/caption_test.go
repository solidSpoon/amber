package session

// caption_test.go：记录里那句人话。
//
// 这一行是读者唯一的入口，所以它只能有两种来源：确定的元素（可访问性树），
// 或者调用方给的原话。定位失败时更要说清"想点的是哪个"，不能退回"点了一下"。

import (
	"strings"
	"testing"

	"github.com/solidSpoon/rigd/internal/inspect"
)

func TestCaptionForUsesWhatWeHave(t *testing.T) {
	button := &inspect.Element{Role: "button", Name: "Save"}
	cases := []struct {
		name string
		step Step
		el   *inspect.Element
		want string
	}{
		{"ref 点中按钮", Step{Tool: "click", Args: map[string]any{"ref": "e3"}}, button, "点击「Save」按钮"},
		{"按文字点，元素还没找到", Step{Tool: "click", Args: map[string]any{"match": "Skip"}}, nil, "点击「Skip」"},
		{"按选择器点", Step{Tool: "click", Args: map[string]any{"selector": "#go"}}, nil, "点击 #go"},
		{"只有引用号", Step{Tool: "click", Args: map[string]any{"ref": "e9"}}, nil, "点击 e9"},
		{"只有坐标", Step{Tool: "click", Args: map[string]any{"x": 10, "y": 20}}, nil, "点击 (10, 20)"},
		{"悬停按文字", Step{Tool: "hover", Args: map[string]any{"match": "serendipity"}}, nil, "移入「serendipity」"},
		{"按文字填表", Step{Tool: "fill", Args: map[string]any{"match": "Search", "text": "dog"}}, nil, "在「Search」填入 「dog」"},
		{"按引用号填表", Step{Tool: "fill", Args: map[string]any{"ref": "e2", "text": "hi"}}, nil, "在 e2 填入 「hi」"},
		{"capture", Step{Tool: "capture"}, nil, "看一眼当前界面"},
		{"sleep 不带空转", Step{Tool: "sleep", Args: map[string]any{"ms": 300}}, nil, "暂停 300 毫秒"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captionFor(tc.step, tc.el); got != tc.want {
				t.Fatalf("caption = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 元素有了名字就优先用名字：引用号对读者没有意义。
func TestCaptionPrefersElementName(t *testing.T) {
	step := Step{Tool: "click", Args: map[string]any{"ref": "q1", "match": "Skip"}}
	got := captionFor(step, &inspect.Element{Role: "button", Name: "Skip"})
	if got != "点击「Skip」按钮" {
		t.Fatalf("caption = %q", got)
	}
	if strings.Contains(got, "q1") {
		t.Fatalf("不该把引用号写进记录：%q", got)
	}
}
