package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/store"
)

func call(rx string, dur float64, text string) *store.Call {
	c := &store.Call{ID: 42, ReceiverID: rx, Channel: "Fire", StartedAt: "2026-09-15T10:00:00.000+09:00", Duration: dur, ASRStatus: "done"}
	if text != "" {
		c.Transcript = &text
	}
	return c
}

func TestShouldNotify(t *testing.T) {
	n := config.Notify{MinDurationSec: 1.0}
	w := config.Webhook{Enabled: true, URL: "https://x", Timing: "on_transcript", KeywordMode: "any"}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "hello")); !ok {
		t.Fatal("basic transcript should pass")
	}
	if ok, _ := ShouldNotify(w, n, "call", call("a", 3, "")); ok {
		t.Fatal("on_transcript must ignore call event")
	}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 0.5, "hello")); ok {
		t.Fatal("min duration")
	}
	w.ReceiverIDs = []string{"b"}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "hello")); ok {
		t.Fatal("receiver filter")
	}
	w.ReceiverIDs = nil
	w.Keywords = []string{"fire", "救急"}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "there is a FIRE")); !ok {
		t.Fatal("keyword any (case-insensitive)")
	}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "nothing")); ok {
		t.Fatal("keyword any miss")
	}
	w.KeywordMode = "exclude"
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "there is a fire")); ok {
		t.Fatal("keyword exclude")
	}
	if ok, _ := ShouldNotify(w, n, "transcript", call("a", 3, "nothing")); !ok {
		t.Fatal("keyword exclude pass")
	}
	// on_call ignores keywords
	w.Timing = "on_call"
	w.KeywordMode = "any"
	if ok, _ := ShouldNotify(w, n, "call", call("a", 3, "")); !ok {
		t.Fatal("on_call should ignore keywords")
	}
	// both → transcript event edits
	w.Timing = "both"
	w.Keywords = nil
	if ok, edit := ShouldNotify(w, n, "call", call("a", 3, "")); !ok || edit {
		t.Fatal("both/call")
	}
	if ok, edit := ShouldNotify(w, n, "transcript", call("a", 3, "x")); !ok || !edit {
		t.Fatal("both/transcript should edit")
	}
	w.Enabled = false
	if ok, _ := ShouldNotify(w, n, "call", call("a", 3, "")); ok {
		t.Fatal("disabled")
	}
}

func TestRateLimitMerge(t *testing.T) {
	s := New(func() config.Config { return config.Default() }, func(string) Templates { return Templates{} }, func() string { return "" }, nil)
	for i := 0; i < 3; i++ {
		if !s.allowSend("w", 3) {
			t.Fatalf("send %d should be allowed", i)
		}
	}
	if s.allowSend("w", 3) {
		t.Fatal("4th should be merged")
	}
	if s.allowSend("w", 3) {
		t.Fatal("5th should be merged")
	}
	if n := s.takeMerged("w"); n != 2 {
		t.Fatalf("merged=%d", n)
	}
	if n := s.takeMerged("w"); n != 0 {
		t.Fatalf("merged should reset, got %d", n)
	}
	// window expiry
	s.mu.Lock()
	for i := range s.sent["w"] {
		s.sent["w"][i] = time.Now().Add(-2 * time.Minute)
	}
	s.mu.Unlock()
	if !s.allowSend("w", 3) {
		t.Fatal("after window expiry should be allowed")
	}
}

func TestDiscordPayload(t *testing.T) {
	tm := Templates{Transcribing: "transcribing", FieldTime: "time", FieldLength: "len", FieldPeak: "peak", Truncated: "(more)", MoreCalls: "{n} more", OpenInUI: "open", Seconds: "%.1f s"}
	peak := -12.3
	c := call("a", 3.5, "hello world")
	c.PeakDB = &peak
	j := Job{Event: "transcript", Call: c, Receiver: config.Receiver{ID: "a", Name: "Fire", Color: "#ff0000"}, Webhook: config.Webhook{Mention: "@here"}}
	p := DiscordPayload(j, tm, "http://h:8000/#/calls/42", 3)
	b, _ := json.Marshal(p)
	s := string(b)
	for _, want := range []string{`"title":"Fire"`, `"description":"hello world"`, `"color":16711680`, `"url":"http://h:8000/#/calls/42"`, `"3 more"`, `@here`, `"-12.3 dB"`, `"3.5 s"`} {
		if !strings.Contains(s, want) {
			t.Errorf("payload missing %s: %s", want, s)
		}
	}
	// on_call → transcribing placeholder
	j.Event = "call"
	p = DiscordPayload(j, tm, "", 0)
	if p["embeds"].([]any)[0].(map[string]any)["description"] != "transcribing" {
		t.Fatal("on_call description")
	}
	// truncation
	long := strings.Repeat("あ", 2500)
	c2 := call("a", 3, long)
	j2 := Job{Event: "transcript", Call: c2}
	p = DiscordPayload(j2, tm, "", 0)
	d := p["embeds"].([]any)[0].(map[string]any)["description"].(string)
	if len([]rune(d)) > 2000 || !strings.HasSuffix(d, "(more)") {
		t.Fatalf("truncation failed: len=%d", len([]rune(d)))
	}
	g := GenericPayload(j, "http://h:8000/#/calls/42")
	if g["event"] != "call" || g["audio_url"] != "http://h:8000/api/calls/42/audio" {
		t.Fatalf("generic: %+v", g)
	}
}
