// ambericon 用代码画出 Amber 的图标，并一次性生成所有落地文件。
//
// 为什么用代码画而不是手写 SVG：
//   - 一张图标要出多个产物（应用图标、favicon、位图）。手抄几何必然漂移：
//     之前出过事故——favicon 的坐标是人工除以 16，忘了先减掉 100 的安全边，
//     结果大图看着是直线、小图成了折线。
//   - 切面的弧线要算：贝塞尔控制点挂在弦的法向上，改一个 bow 值就是另一条弧。
//     手写的话每改一次都要重新推control point。
//   - 三个产物共用同一份参数，不存在"两份几何各改各的"。
//
// 用法：
//
//	go run ./cmd/ambericon                  # 写 SVG，并在有 rsvg-convert 时顺带导出 PNG
//	go run ./cmd/ambericon -bow 0           # 切割线改成直线
//	go run ./cmd/ambericon -out /tmp/x      # 换个输出目录
//	go run ./cmd/ambericon -png=false       # 只要 SVG
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- 参数

// palette 一套配色。深色版是主版，浅色版只是换这一组值。
type palette struct {
	board  string // 板子
	facet  string // 切面
	glint  string // 棱上的反光
	inclus string // 内含物
	glintO float64
}

// palettes 可选的几套色。形状不变，只换这一组值——图标就是一块料 + 一个被封住的东西，
// 料是什么颜色，观感就完全换一个人。
//
// 默认用 blue：蓝珀是真实品种（多米尼加产，光下透出青蓝），所以叫 Amber 不算说谎；
// amber 那套是原名对应色，留着做备选。
var palettes = map[string]palette{
	"amber":  {"#CE8030", "#F2C078", "#FFDCA8", "#402B14", 0.45},
	"blue":   {"#1C5568", "#4E97AB", "#C9EDF3", "#041A22", 0.40},
	"green":  {"#2C5F46", "#6FA87F", "#D3F0D8", "#0D2A1A", 0.40},
	"indigo": {"#2E3E77", "#5A6BB0", "#CFD6F2", "#121838", 0.38},
	"slate":  {"#2B3138", "#59636E", "#DCE3EA", "#11151A", 0.35},
}

const (
	canvas = 1024.0 // macOS 图标画布
	margin = 100.0  // 四边安全边（Apple 模板）
	side   = canvas - 2*margin
	radius = 185.4 // 底板圆角，Apple 模板值

	cutTop  = 560.0 // 切割线在上边的落点：从左上角往右 560
	cutLeft = 460.0 // 切割线在左边的落点：从左上角往下 460

	inclusR  = 92.0
	inclusCX = 600.0
	inclusCY = 690.0

	glintW = 3.0
)

// cutBow 切割线的弯曲量：控制点沿弦的法向偏移多少。
// 0 = 直线；负值向底板内侧鼓（浅色面变大），正值反过来。
//
// 定在 -38：
//
//	· 0 的时候这条线在大尺寸下读作“纸被折过”，不像被切开的材料；
//	· -90 已经是一个图形手势（波浪），再大就开始像镜头光晕；
//	· -38 刚好在原地转个弯，放进 32px 也不抢形状。
//
// 想重温这几个点：-bow 0 / -38 / -90 / -160 各跑一次对比。
var cutBow = -38.0

// ---------------------------------------------------------------- 几何

type point struct{ x, y float64 }

func (p point) sub(q point) point     { return point{p.x - q.x, p.y - q.y} }
func (p point) add(q point) point     { return point{p.x + q.x, p.y + q.y} }
func (p point) scale(k float64) point { return point{p.x * k, p.y * k} }
func (p point) norm() point           { return p.scale(1 / math.Hypot(p.x, p.y)) }
func (p point) perp() point           { return point{-p.y, p.x} }
func (p point) String() string        { return fmt.Sprintf("%s %s", num(p.x), num(p.y)) }

// cut 切割线的三次贝塞尔：起点在上边，终点在左边，两个控制点挂在法向上。
// 弦长固定，控制点各在 1/3、2/3 处平移 bow，因此 bow=0 时退化成一条直线。
func cut() (a, c1, c2, b point) {
	a = point{margin + cutTop, margin}
	b = point{margin, margin + cutLeft}
	chord := b.sub(a)
	n := chord.perp().norm()
	c1 = a.add(chord.scale(1.0 / 3)).add(n.scale(cutBow))
	c2 = a.add(chord.scale(2.0 / 3)).add(n.scale(cutBow))
	return a, c1, c2, b
}

// facetPath 浅色面的填充路径：左上角 → 上边落点 → 弧 → 左边落点 → 沿左边封闭。
func facetPath() string {
	a, c1, c2, b := cut()
	var s strings.Builder
	fmt.Fprintf(&s, "M%s %sL%s C%s %s %sZ", num(margin), num(margin), a.String(), c1.String(), c2.String(), b.String())
	return s.String()
}

// glintPath 同一条弧，用来描棱上的反光（不封闭）。
func glintPath() string {
	a, c1, c2, b := cut()
	return fmt.Sprintf("M%s C%s %s %s", a.String(), c1.String(), c2.String(), b.String())
}

// num 输出人类可读的数字：去掉多余小数位，避免 0.30000000000000004 这种。
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// ---------------------------------------------------------------- 输出

// icon 一份图标参数。应用图标和 favicon 的差别只有 viewBox：
// favicon 直接用 100 100 824 824 取版心，一个坐标都不用换算，所以不可能走样。
type icon struct {
	file    string
	viewBox string
	size    int
	pal     palette
}

func render(ic icon) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!-- 由 cmd/ambericon 生成，不要手改；要改形状/配色请改那个文件。 -->\n")
	fmt.Fprintf(&b, "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=%q width=\"%d\" height=\"%d\" role=\"img\" aria-label=\"Amber\">\n",
		ic.viewBox, ic.size, ic.size)
	fmt.Fprintf(&b, "  <defs>\n    <clipPath id=\"tile\">\n      <rect x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\" rx=\"%s\"/>\n    </clipPath>\n  </defs>\n",
		num(margin), num(margin), num(side), num(side), num(radius))
	b.WriteString("  <g clip-path=\"url(#tile)\">\n")
	fmt.Fprintf(&b, "    <rect x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\" fill=\"%s\"/>\n",
		num(margin), num(margin), num(side), num(side), ic.pal.board)
	fmt.Fprintf(&b, "    <path d=%q fill=%q/>\n", facetPath(), ic.pal.facet)
	fmt.Fprintf(&b, "    <path d=%q fill=\"none\" stroke=%q stroke-width=%q opacity=%q/>\n",
		glintPath(), ic.pal.glint, num(glintW), num(ic.pal.glintO))
	fmt.Fprintf(&b, "    <circle cx=\"%s\" cy=\"%s\" r=\"%s\" fill=%q/>\n",
		num(inclusCX), num(inclusCY), num(inclusR), ic.pal.inclus)
	b.WriteString("  </g>\n</svg>\n")
	return b.String()
}

func main() {
	out := flag.String("out", "web/public", "输出目录")
	withPNG := flag.Bool("png", true, "有 rsvg-convert 时顺带导出位图")
	flag.Float64Var(&cutBow, "bow", cutBow, "切割线弯曲量：0 为直线，负值向内侧鼓")
	palName := flag.String("palette", "blue", "配色：blue（蓝珀，默认）| amber | green | indigo | slate")
	flag.Parse()

	pal, ok := palettes[*palName]
	if !ok {
		fail("未知配色 %q，可选：amber blue green indigo slate", *palName)
	}

	app := icon{file: "app-icon.svg", viewBox: "0 0 1024 1024", size: 1024, pal: pal}
	fav := icon{
		file:    "amber.svg",
		viewBox: fmt.Sprintf("%s %s %s %s", num(margin), num(margin), num(side), num(side)),
		size:    64,
		pal:     pal,
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail("建输出目录: %v", err)
	}
	for _, ic := range []icon{app, fav} {
		path := filepath.Join(*out, ic.file)
		if err := os.WriteFile(path, []byte(render(ic)), 0o644); err != nil {
			fail("写 %s: %v", ic.file, err)
		}
		fmt.Println("写", path)
	}

	if !*withPNG {
		return
	}
	if _, err := exec.LookPath("rsvg-convert"); err != nil {
		fmt.Println("跳过位图：没找到 rsvg-convert（SVG 已经写好了）")
		return
	}
	dst := filepath.Join(*out, "app-icon-1024.png")
	cmd := exec.Command("rsvg-convert", "-w", "1024", "-h", "1024", filepath.Join(*out, app.file), "-o", dst)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		fail("导出位图: %v %s", err, outBytes)
	}
	fmt.Println("写", dst)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ambericon: "+format+"\n", args...)
	os.Exit(1)
}
