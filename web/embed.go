// Package web embeds the static web UI.
package web

import "embed"

//go:embed index.html app.js style.css fonts
var FS embed.FS
