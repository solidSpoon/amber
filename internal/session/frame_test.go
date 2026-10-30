package session

import (
	"testing"

	"github.com/solidSpoon/rigd/internal/store"
)

// cropdetect 的输出是一堆 INFO 行，这里验证「取并集」和「贴边就当铺满」两条规则。
func TestParseCropBox(t *testing.T) {
	display := store.Rect{Width: 1920, Height: 1080}

	corner := `frame=  1 fps=0.0 q=0.0 size=N/A time=00:00:12.50 bitrate=N/A
[Parsed_cropdetect_1 @ 0x1] x1:0 x2:959 y1:0 y2:539 w:960 h:540 x:0 y:0 crop=960:540:0:0
[Parsed_cropdetect_1 @ 0x1] x1:0 x2:959 y1:0 y2:539 w:960 h:540 x:0 y:0 crop=960:540:0:0
`
	frame, ok := parseCropBox(corner, display)
	if !ok {
		t.Fatal("应当能量出窗口区域")
	}
	if frame != (store.Rect{X: 0, Y: 0, Width: 960, Height: 540}) {
		t.Fatalf("窗口区域不对：%+v", frame)
	}

	// 一帧量到小窗、另一帧量到更大的区域：并集才是「这段录像覆盖的最大有效区域」
	mixed := `crop=960:540:0:0
crop=1200:700:0:0
`
	if got, ok := parseCropBox(mixed, display); !ok || got.Width != 1200 || got.Height != 700 {
		t.Fatalf("并集应取最大区域，得到 %+v", got)
	}

	// 贴合整幅画面（差几个像素）应该归成「铺满」，否则界面会做无意义的缩放
	full := `crop=1912:1074:2:2
`
	if got, ok := parseCropBox(full, display); !ok || got != display {
		t.Fatalf("贴边应归为铺满，得到 %+v", got)
	}

	if _, ok := parseCropBox("no crop line here", display); ok {
		t.Fatal("没有 crop 行时不应返回几何")
	}
}
