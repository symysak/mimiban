// Package web embeds the single-page UI and its translation dictionaries.
package web

import "embed"

// FS contains index.html and locales/*.json.
//
//go:embed index.html locales/*.json
var FS embed.FS
