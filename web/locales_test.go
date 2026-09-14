package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func load(t *testing.T, name string) map[string]string {
	b, err := FS.ReadFile("locales/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func TestLocaleKeysMatch(t *testing.T) {
	ja := load(t, "ja.json")
	en := load(t, "en.json")
	for k := range ja {
		if _, ok := en[k]; !ok {
			t.Errorf("en.json missing %q", k)
		}
	}
	for k := range en {
		if _, ok := ja[k]; !ok {
			t.Errorf("ja.json missing %q", k)
		}
	}
	for k, v := range en {
		if strings.TrimSpace(v) == "" {
			t.Errorf("en.json empty value for %q", k)
		}
	}
}

// Every key referenced statically in index.html must exist in both dictionaries.
func TestIndexKeysExist(t *testing.T) {
	html, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	ja := load(t, "ja.json")
	re := regexp.MustCompile(`data-i18n(?:-placeholder|-title)?="([a-z_.]+)"|\bt\('([a-z_.]+)'`)
	for _, m := range re.FindAllStringSubmatch(string(html), -1) {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if strings.HasSuffix(k, ".") || strings.HasSuffix(k, "_") { // dynamic prefix like 'state.' / 'rx.ch_'
			continue
		}
		if _, ok := ja[k]; !ok {
			t.Errorf("index.html references unknown key %q", k)
		}
	}
}

// index.html must not contain Japanese literals (all UI text goes through the dictionary).
func TestNoJapaneseLiteralsInHTML(t *testing.T) {
	html, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`[\p{Hiragana}\p{Katakana}\p{Han}]+`)
	for i, line := range strings.Split(string(html), "\n") {
		if m := re.FindString(line); m != "" {
			// The language picker deliberately shows "日本語" as a language name.
			if strings.Contains(line, `<option value="ja">日本語</option>`) {
				continue
			}
			t.Errorf("line %d has Japanese literal %q", i+1, m)
		}
	}
}
