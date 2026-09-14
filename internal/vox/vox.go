// Package vox implements the voice-operated switch state machine that cuts a
// continuous PCM stream into calls.
package vox

import (
	"math"
	"sync"
	"time"
)

// Params are the tunable VOX values. They can be replaced while running.
type Params struct {
	ThresholdDB  float64
	SilenceSec   float64
	PrebufferSec float64
	MinCallSec   float64
	MaxCallSec   float64
	SampleRate   int
}

// TrailingKeepSec is how much trailing silence is kept at the end of a call.
const TrailingKeepSec = 0.3

// EventKind distinguishes the outputs of Feed.
type EventKind int

// Event kinds.
const (
	EventStart EventKind = iota
	EventEnd
	EventDiscard
)

// Event is emitted by Feed.
type Event struct {
	Kind      EventKind
	StartedAt time.Time
	Samples   []int16 // filled on EventEnd
	Duration  float64 // seconds, on EventEnd / EventDiscard
	PeakDB    float64
	// Reason is "silence" / "max" on EventEnd, "min" on EventDiscard.
	Reason string
}

// Machine is one receiver's VOX state.
type Machine struct {
	mu        sync.Mutex
	p         Params
	levelDB   float64
	recording bool
	startedAt time.Time
	buf       []int16   // current call
	pre       [][]int16 // prebuffer blocks
	preLen    int
	silence   float64 // seconds of continuous silence while recording
	peakDB    float64
	total     float64 // seconds recorded so far
}

// New creates a machine.
func New(p Params) *Machine {
	m := &Machine{levelDB: -100}
	m.SetParams(p)
	return m
}

// SetParams replaces parameters without stopping a running call.
func (m *Machine) SetParams(p Params) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.SampleRate == 0 {
		p.SampleRate = 16000
	}
	if p.MaxCallSec <= 0 {
		p.MaxCallSec = 120
	}
	m.p = p
}

// Params returns the current parameters.
func (m *Machine) Params() Params {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.p
}

// LevelDB returns the RMS level of the last block.
func (m *Machine) LevelDB() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.levelDB
}

// Recording reports whether a call is in progress.
func (m *Machine) Recording() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recording
}

// Elapsed returns seconds since the call started (0 when idle).
func (m *Machine) Elapsed() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.recording {
		return 0
	}
	return m.total
}

// RMSdB computes the RMS level in dBFS of a block.
func RMSdB(s []int16) float64 {
	if len(s) == 0 {
		return -100
	}
	var acc float64
	for _, v := range s {
		f := float64(v)
		acc += f * f
	}
	rms := math.Sqrt(acc / float64(len(s)))
	if rms < 1 {
		return -100
	}
	db := 20 * math.Log10(rms/32768.0)
	if db < -100 {
		return -100
	}
	return db
}

// Feed processes one block. now is the wall-clock time of the block end.
func (m *Machine) Feed(block []int16, now time.Time) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(block) == 0 {
		return nil
	}
	p := m.p
	blockSec := float64(len(block)) / float64(p.SampleRate)
	db := RMSdB(block)
	m.levelDB = db
	loud := db >= p.ThresholdDB
	var events []Event

	if !m.recording {
		if loud {
			m.recording = true
			m.silence = 0
			m.peakDB = db
			m.total = 0
			m.buf = m.buf[:0]
			preSec := 0.0
			for _, b := range m.pre {
				m.buf = append(m.buf, b...)
				preSec += float64(len(b)) / float64(p.SampleRate)
			}
			m.buf = append(m.buf, block...)
			m.total = preSec + blockSec
			m.startedAt = now.Add(-time.Duration(m.total * float64(time.Second)))
			m.pre = m.pre[:0]
			m.preLen = 0
			events = append(events, Event{Kind: EventStart, StartedAt: m.startedAt})
			return events
		}
		// idle: maintain prebuffer
		m.pre = append(m.pre, append([]int16(nil), block...))
		m.preLen += len(block)
		maxLen := int(p.PrebufferSec * float64(p.SampleRate))
		for len(m.pre) > 0 && m.preLen-len(m.pre[0]) >= maxLen {
			m.preLen -= len(m.pre[0])
			m.pre = m.pre[1:]
		}
		return nil
	}

	// recording
	m.buf = append(m.buf, block...)
	m.total += blockSec
	if loud {
		m.silence = 0
		if db > m.peakDB {
			m.peakDB = db
		}
	} else {
		m.silence += blockSec
	}
	if m.silence >= p.SilenceSec {
		events = append(events, m.finish(p, "silence"))
	} else if m.total >= p.MaxCallSec {
		events = append(events, m.finish(p, "max"))
	}
	return events
}

// Flush ends a running call (used when the source stops).
func (m *Machine) Flush() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.recording {
		return nil
	}
	return []Event{m.finish(m.p, "flush")}
}

func (m *Machine) finish(p Params, reason string) Event {
	m.recording = false
	samples := m.buf
	// trim trailing silence, keep TrailingKeepSec
	if reason == "silence" {
		trim := m.silence - TrailingKeepSec
		if trim > 0 {
			n := int(trim * float64(p.SampleRate))
			if n < len(samples) {
				samples = samples[:len(samples)-n]
			}
		}
	}
	dur := float64(len(samples)) / float64(p.SampleRate)
	out := make([]int16, len(samples))
	copy(out, samples)
	m.buf = m.buf[:0]
	m.silence = 0
	ev := Event{StartedAt: m.startedAt, Duration: dur, PeakDB: m.peakDB, Samples: out, Reason: reason}
	if dur < p.MinCallSec {
		ev.Kind = EventDiscard
		ev.Reason = "min"
		ev.Samples = nil
	} else {
		ev.Kind = EventEnd
	}
	return ev
}
