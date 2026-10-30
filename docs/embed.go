// Package docs 承载产品文档：它是产品的定义，页面直接展示这一份，
// 不额外维护第二份副本（两份一定会走样）。
package docs

import _ "embed"

//go:embed PRODUCT.md
var Product string
