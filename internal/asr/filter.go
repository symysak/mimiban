// Package asr drives whisper-server and post-processes its output.
package asr

import (
	"regexp"
	"strings"

	"github.com/symysak/mimiban/internal/config"
)

// Segment is one whisper segment.
type Segment struct {
	Text         string
	NoSpeechProb float64
	HasProb      bool
}

// FilterResult is the outcome of the hallucination filter.
type FilterResult struct {
	Text string
	Note string // "" / "repetition" / "no_speech" / "hallucination"
}

var splitRe = regexp.MustCompile(`[。．.!?！？\n]+`)

// bracketOnly matches whisper's sound descriptions such as "(コメント)",
// "(拍手)", "[音楽]", "【チャイム】" or "♪" with nothing else around them.
var bracketOnly = regexp.MustCompile(`^[\s♪]*[（(\[【][^）)\]】]*[）)\]】][\s。.♪]*$`)

// Filter applies the mandatory hallucination checks:
// (a) 3+ consecutive repeats of a sentence/phrase → empty, note=repetition
// (b) segments with no_speech_prob above threshold are dropped
// (c) results consisting only of known hallucination phrases → empty
func Filter(segs []Segment, phrases []string, noSpeechThreshold float64) FilterResult {
	var kept []string
	dropped := 0
	for _, s := range segs {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		if s.HasProb && noSpeechThreshold > 0 && s.NoSpeechProb > noSpeechThreshold {
			dropped++
			continue
		}
		kept = append(kept, t)
	}
	text := strings.TrimSpace(strings.Join(kept, " "))
	if text == "" {
		if dropped > 0 {
			return FilterResult{Text: "", Note: "no_speech"}
		}
		return FilterResult{}
	}
	if IsRepetitive(text) {
		return FilterResult{Text: "", Note: "repetition"}
	}
	if OnlyHallucinations(text, phrases) || bracketOnly.MatchString(text) {
		return FilterResult{Text: "", Note: "hallucination"}
	}
	// Larger models like to append "ご視聴ありがとうございました" after real
	// speech; drop whole sentences that are known phrases but keep the rest.
	text = TrimHallucinatedSentences(text, phrases)
	return FilterResult{Text: text}
}

// IsRepetitive reports whether the same sentence or word repeats 3+ times in a
// row.
func IsRepetitive(text string) bool {
	// sentence level
	parts := splitRe.Split(text, -1)
	if repeats(parts, 3) {
		return true
	}
	// word level (space separated)
	words := strings.Fields(text)
	if len(words) >= 3 && repeats(words, 3) {
		return true
	}
	// character-run level for CJK without spaces: look for a substring of
	// length 2..8 repeated 3+ times consecutively.
	r := []rune(strings.Join(strings.Fields(text), ""))
	for n := 2; n <= 8 && n*3 <= len(r); n++ {
		for i := 0; i+n*3 <= len(r); i++ {
			a := string(r[i : i+n])
			if a == string(r[i+n:i+2*n]) && a == string(r[i+2*n:i+3*n]) {
				// only count as hallucination if the repeat covers most of the text
				if float64(n*3) >= float64(len(r))*0.6 {
					return true
				}
			}
		}
	}
	return false
}

func repeats(parts []string, n int) bool {
	run := 1
	prev := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == prev {
			run++
			if run >= n {
				return true
			}
		} else {
			run = 1
			prev = p
		}
	}
	return false
}

// OnlyHallucinations reports whether text consists (almost) solely of known
// phrases: after removing every phrase, at most 30% of the original
// characters may remain. This also catches results that merely echo the ASR
// prompt with a stray word or two added.
func OnlyHallucinations(text string, phrases []string) bool {
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(" \u3000。、．，.,!?！？\n\t-…・「」()[]", r) {
				return -1
			}
			return r
		}, s)
	}
	orig := strip(strings.ToLower(text))
	if orig == "" {
		return true
	}
	rest := strings.ToLower(text)
	removed := false
	for _, p := range phrases {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.Contains(rest, p) {
			removed = true
			rest = strings.ReplaceAll(rest, p, " ")
		}
	}
	if !removed {
		return false
	}
	rest = strip(rest)
	return float64(len([]rune(rest))) <= 0.3*float64(len([]rune(orig)))
}

var bracketFrag = regexp.MustCompile(`[（(\[【][^）)\]】]*[）)\]】]`)

var sentenceRe = regexp.MustCompile(`[^。．.!?！？\n]+[。．.!?！？]*`)

// TrimHallucinatedSentences removes sentences that consist solely of known
// phrases (or bracketed sound descriptions) while keeping the other sentences.
func TrimHallucinatedSentences(text string, phrases []string) string {
	var kept []string
	for _, sent := range sentenceRe.FindAllString(text, -1) {
		st := strings.TrimSpace(sent)
		if st == "" {
			continue
		}
		// drop bracketed sound descriptions inside the sentence as well
		st = strings.TrimSpace(bracketFrag.ReplaceAllString(st, ""))
		if st == "" || bracketOnly.MatchString(st) {
			continue
		}
		if len(phrases) > 0 && exactPhrase(st, phrases) {
			continue
		}
		kept = append(kept, st)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

// exactPhrase reports whether sentence equals one of the phrases ignoring
// punctuation, spaces and case.
func exactPhrase(sentence string, phrases []string) bool {
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(" \u3000。、．，.,!?！？\n\t-…・「」()[]", r) {
				return -1
			}
			return r
		}, strings.ToLower(s))
	}
	n := norm(sentence)
	if n == "" {
		return true
	}
	for _, p := range phrases {
		if norm(p) == n {
			return true
		}
	}
	return false
}

// PromptPhrases splits an ASR hint text into sentences so that a result that
// merely echoes the prompt (a common whisper failure on non-speech audio) is
// treated as a hallucination.
func PromptPhrases(prompt string) []string {
	var out []string
	for _, p := range regexp.MustCompile(`[。．.!?！？\n、,]+`).Split(prompt, -1) {
		if p = strings.TrimSpace(p); len([]rune(p)) >= 2 {
			out = append(out, p)
		}
	}
	return out
}

// ApplyDictionary replaces every From with To, longest first.
func ApplyDictionary(text string, dict []config.DictEntry) string {
	if len(dict) == 0 || text == "" {
		return text
	}
	sorted := make([]config.DictEntry, 0, len(dict))
	for _, e := range dict {
		if e.From != "" {
			sorted = append(sorted, e)
		}
	}
	// longest match first to avoid partial overlaps
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && len(sorted[j].From) > len(sorted[j-1].From); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, e := range sorted {
		text = strings.ReplaceAll(text, e.From, e.To)
	}
	return text
}
