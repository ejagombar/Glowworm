// Package htmx embeds the vendored HTMX runtime (doc 04 §1).
package htmx

import "embed"

// FS is the embedded htmx asset tree (web/htmx).
//
//go:embed htmx.min.js
var FS embed.FS