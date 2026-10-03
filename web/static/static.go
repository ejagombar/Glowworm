// Package static embeds the web assets (CSS, JS, fonts).
package static

import "embed"

// FS is the embedded static asset tree (web/static).
//
//go:embed css js fonts
var FS embed.FS
