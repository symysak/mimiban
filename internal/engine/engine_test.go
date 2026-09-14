package engine

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/capture"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/store"
)

// fakeCapture lets tests push blocks.
type fakeCapture struct {
	src     capture.Source
	onState func(string, string)
	ch      chan capture.Block
	started chan struct{}
}

func (f *fakeCapture) Start(ctx context.Context) (<-chan capture.Block, error) {
	f.ch = make(chan capture.Block, 64)
	go func() { <-ctx.Done() }()
	f.onState(capture.StateListening, "")
	close(f.started)
	return f.ch, nil
}
func (f *fakeCapture) Stop() { close(f.ch) }

type fakeEnum struct{}

func (fakeEnum) Enumerate() ([]capture.Device, error) {
	return []capture.Device{{Index: 0, Name: "Stereo USB", Channels: 2}}, nil
}

type fakeASR struct{ text string }

func (f fakeASR) Transcribe(_ context.Context, _ string, _, _ string) ([]asr.Segment, error) {
	return []asr.Segment{{Text: f.text}}, nil
}

type harness struct {
	e        *Engine
	cfg      *config.Store
	db       *store.DB
	mu       sync.Mutex
	captures map[string]*fakeCapture
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	dir := t.TempDir()
	cs, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "t.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{cfg: cs, db: db, captures: map[string]*fakeCapture{}, now: time.Date(2026, 9, 15, 10, 0, 0, 0, time.Local)}
	h.e = New(Options{
		Config: cs, DB: db, BaseDir: dir, Enumerator: fakeEnum{},
		Factory: func(src capture.Source, onState func(string, string)) capture.Capture {
			fc := &fakeCapture{src: src, onState: onState, started: make(chan struct{})}
			h.mu.Lock()
			h.captures[src.DeviceName+"/"+src.Type] = fc
			h.mu.Unlock()
			return fc
		},
		Transcriber: fakeASR{text: "テスト交信"},
		Encoder:     WAVEncoder{},
		Logf:        t.Logf,
		Now:         func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
	})
	// pretend ffmpeg exists so StartReceiver does not refuse
	h.e.ffmpeg = capture.Binary{Name: "ffmpeg", Path: "/fake/ffmpeg", Found: true}
	t.Cleanup(func() { h.e.Stop(); db.Close() })
	return h
}

func (h *harness) addReceiver(t *testing.T, name, ch string) config.Receiver {
	r := config.DefaultReceiver()
	r.ID = "rx_" + name
	r.Name = name
	r.DeviceName = "Stereo USB"
	r.ChannelSelect = ch
	r.VoxThresholdDB = -40
	r.SilenceSec = 0.5
	r.PrebufferSec = 0.2
	r.MinCallSec = 0.5
	cfg := h.cfg.Get()
	if err := ValidateReceiver(cfg, r, "", cfg.Port); err != nil {
		t.Fatalf("validate %s: %v", name, err)
	}
	_ = h.cfg.Update(func(c *config.Config) error { c.Receivers = append(c.Receivers, r); return nil })
	return r
}

// push sends sec seconds of stereo audio with the given amplitudes.
func (h *harness) push(fc *fakeCapture, sec float64, ampL, ampR int16) {
	blocks := int(sec / 0.05)
	n := 16000 / 20
	for i := 0; i < blocks; i++ {
		b := capture.Block{Channels: [][]int16{make([]int16, n), make([]int16, n)}}
		for j := 0; j < n; j++ {
			if j%2 == 0 {
				b.Channels[0][j] = ampL
				b.Channels[1][j] = ampR
			} else {
				b.Channels[0][j] = -ampL
				b.Channels[1][j] = -ampR
			}
		}
		h.mu.Lock()
		h.now = h.now.Add(50 * time.Millisecond)
		h.mu.Unlock()
		fc.ch <- b
	}
}

func waitCalls(t *testing.T, db *store.DB, want int) []*store.Call {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, total, _ := db.ListCalls(store.ListFilter{})
		if total >= want {
			return items
		}
		time.Sleep(20 * time.Millisecond)
	}
	items, total, _ := db.ListCalls(store.ListFilter{})
	t.Fatalf("expected %d calls, got %d", want, total)
	return items
}

func TestValidateCombinations(t *testing.T) {
	cfg := config.Default()
	mk := func(id, ch string) config.Receiver {
		r := config.DefaultReceiver()
		r.ID, r.Name, r.DeviceName, r.ChannelSelect = id, id, "Dev", ch
		return r
	}
	cfg.Receivers = []config.Receiver{mk("l", "left")}
	if err := ValidateReceiver(cfg, mk("m", "mono"), "", 8000); err == nil {
		t.Fatal("mono over left must fail")
	}
	if err := ValidateReceiver(cfg, mk("l2", "left"), "", 8000); err == nil {
		t.Fatal("double left must fail")
	}
	if err := ValidateReceiver(cfg, mk("r", "right"), "", 8000); err != nil {
		t.Fatalf("left+right must pass: %v", err)
	}
	// editing itself is fine
	if err := ValidateReceiver(cfg, mk("l", "left"), "l", 8000); err != nil {
		t.Fatalf("self edit: %v", err)
	}
	cfg.Receivers = []config.Receiver{mk("m", "mono")}
	if err := ValidateReceiver(cfg, mk("l", "left"), "", 8000); err == nil {
		t.Fatal("left over mono must fail")
	}
	s := config.DefaultReceiver()
	s.ID, s.Name, s.SourceType, s.ListenPort = "s", "s", "stream", 8000
	if err := ValidateReceiver(cfg, s, "", 8000); err == nil {
		t.Fatal("port == http port must fail")
	}
	s.ListenPort = 5004
	cfg.Receivers = append(cfg.Receivers, s)
	s2 := s
	s2.ID = "s2"
	if err := ValidateReceiver(cfg, s2, "", 8000); err == nil {
		t.Fatal("duplicate stream port must fail")
	}
	u := config.DefaultReceiver()
	u.ID, u.Name, u.SourceType, u.InputURL = "u", "u", "url", "udp://127.0.0.1:6001"
	cfg.Receivers = append(cfg.Receivers, u)
	u2 := u
	u2.ID = "u2"
	if err := ValidateReceiver(cfg, u2, "", 8000); err == nil {
		t.Fatal("duplicate url must fail")
	}
}

func TestStereoSplitOnlyLeftFires(t *testing.T) {
	h := newHarness(t)
	l := h.addReceiver(t, "left", "left")
	r := h.addReceiver(t, "right", "right")
	if err := h.e.StartReceiver(l.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.e.StartReceiver(r.ID); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if len(h.captures) != 1 {
		t.Fatalf("expected one shared capture, got %d", len(h.captures))
	}
	var fc *fakeCapture
	for _, c := range h.captures {
		fc = c
	}
	h.mu.Unlock()
	<-fc.started
	if fc.src.OutChannels != 2 {
		t.Fatalf("L/R split needs 2 output channels, got %d", fc.src.OutChannels)
	}
	h.push(fc, 1.0, 0, 0)     // quiet
	h.push(fc, 2.0, 8000, 10) // left loud only
	h.push(fc, 1.5, 0, 0)     // silence ends the call
	calls := waitCalls(t, h.db, 1)
	if calls[0].ReceiverID != l.ID {
		t.Fatalf("wrong receiver fired: %s", calls[0].ReceiverID)
	}
	time.Sleep(100 * time.Millisecond)
	_, total, _ := h.db.ListCalls(store.ListFilter{})
	if total != 1 {
		t.Fatalf("right receiver must not fire; total=%d", total)
	}
	st := h.e.Status()
	if st.Receivers[r.ID].State != StListening || st.Receivers[l.ID].State != StListening {
		t.Fatalf("states: %+v", st.Receivers)
	}
	// stopping one keeps the shared capture alive; stopping both closes it
	_ = h.e.StopReceiver(l.ID)
	h.e.mu.Lock()
	n := len(h.e.captures)
	h.e.mu.Unlock()
	if n != 1 {
		t.Fatal("capture should stay open while right is running")
	}
	_ = h.e.StopReceiver(r.ID)
	h.e.mu.Lock()
	n = len(h.e.captures)
	h.e.mu.Unlock()
	if n != 0 {
		t.Fatal("capture should close when unused")
	}
}

func TestASRPipeline(t *testing.T) {
	h := newHarness(t)
	m := h.addReceiver(t, "mono", "mono")
	_ = h.cfg.Update(func(c *config.Config) error {
		c.Dictionary = []config.DictEntry{{From: "テスト", To: "試験"}}
		return nil
	})
	h.e.Start()
	if err := h.e.StartReceiver(m.ID); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	var fc *fakeCapture
	for _, c := range h.captures {
		fc = c
	}
	h.mu.Unlock()
	<-fc.started
	if fc.src.OutChannels != 1 {
		t.Fatalf("mono should open 1 channel, got %d", fc.src.OutChannels)
	}
	h.push(fc, 0.5, 0, 0)
	h.push(fc, 1.0, 6000, 6000)
	h.push(fc, 1.0, 0, 0)
	waitCalls(t, h.db, 1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, _, _ := h.db.ListCalls(store.ListFilter{})
		if items[0].ASRStatus == "done" {
			if items[0].Transcript == nil || *items[0].Transcript != "試験交信" {
				t.Fatalf("dictionary not applied: %v", items[0].Transcript)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("asr never completed")
}
