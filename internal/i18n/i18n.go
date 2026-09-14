// Package i18n loads the server-side notification templates. UI strings live
// in web/locales and are handled entirely by the browser.
package i18n

import (
	"embed"
	"encoding/json"
	"sync"

	"github.com/symysak/mimiban/internal/notify"
)

//go:embed all:locales
var fs embed.FS

var (
	once  sync.Once
	cache map[string]notify.Templates
)

func load() {
	cache = map[string]notify.Templates{}
	for _, lang := range []string{"ja", "en"} {
		b, err := fs.ReadFile("locales/" + lang + "/notify.json")
		if err != nil {
			continue
		}
		var t notify.Templates
		if json.Unmarshal(b, &t) == nil {
			cache[lang] = t
		}
	}
}

// Templates returns the notification templates for lang (fallback en).
func Templates(lang string) notify.Templates {
	once.Do(load)
	if t, ok := cache[lang]; ok {
		return t
	}
	return cache["en"]
}

// Languages lists the available template languages.
func Languages() []string {
	once.Do(load)
	out := make([]string, 0, len(cache))
	for k := range cache {
		out = append(out, k)
	}
	return out
}
