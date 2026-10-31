package inspect

// inspect_test.go：两段 JS 共用的那套"什么算元素"的判断不能被悄悄拆开。
//
// 这两段脚本跑在页面里，Go 这边测不到行为，但能测"它们还是同一份"：
// 一旦有人只改其中一段（以前就是这样，SEL 一模一样写了两遍），
// 快照列得出的元素就会和按文字定位找得到的元素对不上。

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestScriptsShareElementHelpers(t *testing.T) {
	if strings.Count(snapshotScript, jsElementHelpers) != 1 {
		t.Fatal("快照脚本没有用共用的元素判断")
	}
	if strings.Count(locateScript, jsElementHelpers) != 1 {
		t.Fatal("定位脚本没有用共用的元素判断")
	}
}

func TestLocateScriptFormats(t *testing.T) {
	match, _ := json.Marshal("Skip")
	selector, _ := json.Marshal("")
	script := fmt.Sprintf(locateScript, match, selector)
	if strings.Contains(script, "%!") {
		t.Fatalf("模板占位符和脚本内容打架了：%s", script)
	}
	if !strings.Contains(script, `const wanted = "Skip";`) {
		t.Fatalf("文字没有按 JSON 字面量填进去：%s", script)
	}
}

// 元素中心点只算一次：点、悬停、填写都靠它，算错一起错。
func TestElementCenter(t *testing.T) {
	el := Element{Box: [4]int{10, 20, 100, 50}}
	x, y := el.center()
	if x != 60 || y != 45 {
		t.Fatalf("中心点 = (%v, %v)，期望 (60, 45)", x, y)
	}
}
