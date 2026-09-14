package asr

import (
	"testing"

	"github.com/symysak/mimiban/internal/config"
)

func segs(texts ...string) []Segment {
	out := make([]Segment, len(texts))
	for i, t := range texts {
		out[i] = Segment{Text: t}
	}
	return out
}

func TestFilterHallucinationJaEn(t *testing.T) {
	ja := config.DefaultHallucinations["ja"]
	en := config.DefaultHallucinations["en"]
	r := Filter(segs("ご視聴ありがとうございました。"), ja, 0.7)
	if r.Text != "" || r.Note != "hallucination" {
		t.Fatalf("ja phrase not filtered: %+v", r)
	}
	// same text with the en list must survive (list switching)
	r = Filter(segs("ご視聴ありがとうございました。"), en, 0.7)
	if r.Text == "" {
		t.Fatalf("en list should not filter ja phrase")
	}
	r = Filter(segs("Thanks for watching!"), en, 0.7)
	if r.Text != "" || r.Note != "hallucination" {
		t.Fatalf("en phrase not filtered: %+v", r)
	}
	r = Filter(segs("こちら本部、了解。ご視聴ありがとうございました。"), ja, 0.7)
	if r.Text == "" {
		t.Fatalf("mixed content must be kept: %+v", r)
	}
}

func TestPromptEcho(t *testing.T) {
	phrases := append(config.DefaultHallucinations["ja"], PromptPhrases(config.DefaultPrompts["ja"][0])...)
	r := Filter(segs("さて、こちら、送れ、以上。"), phrases, 0)
	if r.Text != "" || r.Note != "hallucination" {
		t.Fatalf("prompt echo should be dropped: %+v", r)
	}
	r = Filter(segs("こちら本部、現場へ送れ。"), phrases, 0)
	if r.Text == "" {
		t.Fatalf("real sentence containing prompt words must survive: %+v", r)
	}
}

func TestTrailingHallucinationTrimmed(t *testing.T) {
	ja := config.DefaultHallucinations["ja"]
	r := Filter(segs("本日午後6時より、夏祭りが開催されます。", " ご視聴ありがとうございました。"), ja, 0)
	if r.Text != "本日午後6時より、夏祭りが開催されます。" || r.Note != "" {
		t.Fatalf("trailing phrase should be trimmed: %+v", r)
	}
	r = Filter(segs("(音楽) ぜひご来場ください。 ご視聴ありがとうございました"), ja, 0)
	if r.Text != "ぜひご来場ください。" {
		t.Fatalf("bracket + trailing: %+v", r)
	}
}

func TestBracketOnly(t *testing.T) {
	for _, in := range []string{"(コメント)", "（拍手）", "[音楽]", "【チャイム】。", "♪(音楽)♪"} {
		if r := Filter(segs(in), nil, 0); r.Text != "" || r.Note != "hallucination" {
			t.Errorf("%q should be dropped: %+v", in, r)
		}
	}
	if r := Filter(segs("(チャイム) こちら市役所です"), nil, 0); r.Text == "" {
		t.Fatalf("bracket plus speech must survive: %+v", r)
	}
}

func TestFilterRepetition(t *testing.T) {
	r := Filter(segs("了解です。了解です。了解です。了解です。"), nil, 0)
	if r.Text != "" || r.Note != "repetition" {
		t.Fatalf("sentence repetition: %+v", r)
	}
	r = Filter(segs("あああああああああああああ"), nil, 0)
	if r.Text != "" || r.Note != "repetition" {
		t.Fatalf("char repetition: %+v", r)
	}
	r = Filter(segs("go go go now"), nil, 0)
	if r.Text != "" {
		t.Fatalf("word repetition: %+v", r)
	}
	r = Filter(segs("了解、了解。こちら本部です。"), nil, 0)
	if r.Text == "" {
		t.Fatalf("two repeats are fine: %+v", r)
	}
}

func TestFilterNoSpeech(t *testing.T) {
	s := []Segment{{Text: "noise", NoSpeechProb: 0.9, HasProb: true}, {Text: "real words", NoSpeechProb: 0.1, HasProb: true}}
	r := Filter(s, nil, 0.7)
	if r.Text != "real words" {
		t.Fatalf("no_speech filter: %+v", r)
	}
	r = Filter(s[:1], nil, 0.7)
	if r.Text != "" || r.Note != "no_speech" {
		t.Fatalf("all dropped: %+v", r)
	}
}

func TestDictionary(t *testing.T) {
	d := []config.DictEntry{{From: "本部", To: "本部（HQ）"}, {From: "こちら本部", To: "こちらHQ"}}
	if got := ApplyDictionary("こちら本部です", d); got != "こちらHQです" {
		t.Fatalf("longest first: %q", got)
	}
	if got := ApplyDictionary("", d); got != "" {
		t.Fatal("empty")
	}
}

func TestParseResponse(t *testing.T) {
	raw := []byte(`{"text":" hello world","segments":[{"text":" hello","no_speech_prob":0.1},{"text":" world","no_speech_prob":0.2}]}`)
	s, err := ParseResponse(raw)
	if err != nil || len(s) != 2 || !s[0].HasProb {
		t.Fatalf("%v %+v", err, s)
	}
	s, err = ParseResponse([]byte(`{"text":"only text"}`))
	if err != nil || len(s) != 1 || s[0].Text != "only text" {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err := ParseResponse([]byte(`{"error":"boom"}`)); err == nil {
		t.Fatal("error expected")
	}
}
