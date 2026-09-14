package vox

import (
	"math/rand"
	"testing"
	"time"
)

const rate = 16000

func noise(amp int16, n int, r *rand.Rand) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(r.Intn(int(amp)*2+1)) - amp
	}
	return out
}

func feedSeconds(m *Machine, sec float64, amp int16, r *rand.Rand, now *time.Time) []Event {
	var evs []Event
	blocks := int(sec / 0.05)
	for i := 0; i < blocks; i++ {
		*now = now.Add(50 * time.Millisecond)
		evs = append(evs, m.Feed(noise(amp, rate/20, r), *now)...)
	}
	return evs
}

func params() Params {
	return Params{ThresholdDB: -40, SilenceSec: 1.0, PrebufferSec: 0.5, MinCallSec: 0.7, MaxCallSec: 5, SampleRate: rate}
}

func TestRMSdB(t *testing.T) {
	if RMSdB(make([]int16, 800)) != -100 {
		t.Fatal("silence should be -100")
	}
	full := make([]int16, 800)
	for i := range full {
		full[i] = 32767
	}
	if db := RMSdB(full); db > 0.01 || db < -0.01 {
		t.Fatalf("full scale = %f", db)
	}
}

func TestStartStopWithPrebuffer(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	m := New(params())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	// 2 s quiet
	if evs := feedSeconds(m, 2, 20, r, &now); len(evs) != 0 {
		t.Fatalf("quiet produced events: %+v", evs)
	}
	// 2 s loud → start
	evs := feedSeconds(m, 2, 8000, r, &now)
	if len(evs) != 1 || evs[0].Kind != EventStart {
		t.Fatalf("expected start, got %+v", evs)
	}
	if !m.Recording() {
		t.Fatal("should be recording")
	}
	// 1.5 s quiet → end after 1.0 s silence
	evs = feedSeconds(m, 1.5, 20, r, &now)
	if len(evs) != 1 || evs[0].Kind != EventEnd || evs[0].Reason != "silence" {
		t.Fatalf("expected end, got %+v", evs)
	}
	e := evs[0]
	// duration ≈ prebuffer 0.5 + loud 2.0 + trailing 0.3
	if e.Duration < 2.6 || e.Duration > 3.0 {
		t.Fatalf("duration %f", e.Duration)
	}
	if len(e.Samples) != int(e.Duration*rate) {
		t.Fatalf("samples %d vs dur %f", len(e.Samples), e.Duration)
	}
	if e.PeakDB < -20 || e.PeakDB > 0 {
		t.Fatalf("peak %f", e.PeakDB)
	}
	// started_at should be ~2.5 s before the end of the loud part (2 s loud + 0.5 prebuffer)
	if m.Recording() {
		t.Fatal("should be idle")
	}
}

func TestMinCallDiscard(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	p := params()
	p.PrebufferSec = 0
	m := New(p)
	now := time.Now()
	feedSeconds(m, 1, 20, r, &now)
	evs := feedSeconds(m, 0.1, 8000, r, &now) // 100 ms burst
	if len(evs) != 1 || evs[0].Kind != EventStart {
		t.Fatalf("start expected: %+v", evs)
	}
	evs = feedSeconds(m, 1.5, 20, r, &now)
	if len(evs) != 1 || evs[0].Kind != EventDiscard {
		t.Fatalf("discard expected: %+v", evs)
	}
}

func TestMaxCallCut(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	m := New(params())
	now := time.Now()
	evs := feedSeconds(m, 7, 8000, r, &now) // continuous loud > max 5 s
	var ends int
	for _, e := range evs {
		if e.Kind == EventEnd {
			ends++
			if e.Reason != "max" {
				t.Fatalf("reason %s", e.Reason)
			}
			if e.Duration < 4.9 || e.Duration > 5.2 {
				t.Fatalf("duration %f", e.Duration)
			}
		}
	}
	if ends != 1 {
		t.Fatalf("expected exactly one max cut, got %d (events %d)", ends, len(evs))
	}
	// still loud → a second call has started
	if !m.Recording() {
		t.Fatal("should have restarted recording")
	}
}

func TestSetParamsLive(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	m := New(params())
	now := time.Now()
	feedSeconds(m, 1, 300, r, &now) // ~ -40 dB-ish noise? amplitude 300 → about -41 dB
	if m.Recording() {
		t.Fatal("should not trigger at -40 threshold")
	}
	p := params()
	p.ThresholdDB = -60
	m.SetParams(p)
	evs := feedSeconds(m, 0.2, 300, r, &now)
	if len(evs) == 0 || evs[0].Kind != EventStart {
		t.Fatal("lowered threshold should trigger")
	}
}
