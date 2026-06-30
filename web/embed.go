package web

import "embed"

// DistFS 嵌入前端构建产物(web/dist)
// 构建前请先执行: cd web && bun run build
//
//go:embed dist
var DistFS embed.FS
