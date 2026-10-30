// Package web 把构建好的前端静态资源编进二进制。
//
// 单文件发布的好处：管理台不需要额外的 nginx / 静态目录，
// 拷一个 rigd 到机器上就能跑（前端由 `make web` 生成到 dist/）。
package web

import "embed"

// Dist 前端构建产物（Vite 输出目录）。
//
//go:embed all:dist
var Dist embed.FS
