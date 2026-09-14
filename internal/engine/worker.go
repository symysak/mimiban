package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/symysak/mimiban/internal/capture"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/store"
	"github.com/symysak/mimiban/internal/vox"
)

// worker is the per-receiver VOX + save pipeline.
type worker struct {
	e          *Engine
	id         string
	mu         sync.Mutex
	rx         config.Receiver
	vox        *vox.Machine
	shared     *sharedCapture
	capState   string
	lastErr    string
	lastPacket time.Time
	saving     sync.WaitGroup
}

func newWorker(e *Engine, r config.Receiver, sampleRate int) *worker {
	return &worker{
		e:   e,
		id:  r.ID,
		rx:  r,
		vox: vox.New(voxParams(r, sampleRate)),
	}
}

func voxParams(r config.Receiver, sampleRate int) vox.Params {
	return vox.Params{
		ThresholdDB:  r.VoxThresholdDB,
		SilenceSec:   r.SilenceSec,
		PrebufferSec: r.PrebufferSec,
		MinCallSec:   r.MinCallSec,
		MaxCallSec:   r.MaxCallSec,
		SampleRate:   sampleRate,
	}
}

// setParams applies edited receiver settings live (VOX values, name, etc.).
func (w *worker) setParams(r config.Receiver) {
	w.mu.Lock()
	w.rx = r
	w.mu.Unlock()
	w.vox.SetParams(voxParams(r, w.e.o.Config.Get().SampleRate))
}

func (w *worker) setCaptureState(state, lastErr string) {
	w.mu.Lock()
	w.capState = state
	w.lastErr = lastErr
	w.mu.Unlock()
}

// feed selects the receiver's channel from the block and runs the VOX.
func (w *worker) feed(b capture.Block, now time.Time) {
	w.mu.Lock()
	sel := w.rx.ChannelSelect
	w.lastPacket = now
	w.mu.Unlock()
	var samples []int16
	switch {
	case sel == "left" && len(b.Channels) >= 1:
		samples = b.Channels[0]
	case sel == "right" && len(b.Channels) >= 2:
		samples = b.Channels[1]
	case sel == "right":
		samples = b.Channels[0]
	default:
		samples = capture.Mono(b)
	}
	for _, ev := range w.vox.Feed(samples, now) {
		w.handle(ev)
	}
}

// flush ends a running call when the source stops.
func (w *worker) flush() {
	for _, ev := range w.vox.Flush() {
		w.handle(ev)
	}
}

func (w *worker) handle(ev vox.Event) {
	switch ev.Kind {
	case vox.EventStart:
		// nothing persistent yet
	case vox.EventDiscard:
	case vox.EventEnd:
		samples := ev.Samples
		w.saving.Add(1)
		go func() {
			defer w.saving.Done()
			w.save(ev, samples)
		}()
	}
}

func (w *worker) save(ev vox.Event, samples []int16) {
	w.mu.Lock()
	rx := w.rx
	w.mu.Unlock()
	cfg := w.e.o.Config.Get()
	dir := filepath.Join(w.e.RecordDir(), rx.ID, ev.StartedAt.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.e.o.Logf("mkdir %s: %v", dir, err)
		return
	}
	name := ev.StartedAt.Format("20060102_150405.000")
	name = name[:15] + "_" + name[16:] + ".mp3"
	path := filepath.Join(dir, name)
	if err := w.e.o.Encoder.Encode(samples, cfg.SampleRate, cfg.MP3Bitrate, path); err != nil {
		w.e.o.Logf("encode %s: %v", path, err)
		return
	}
	status := "pending"
	if !rx.ASREnabled || !cfg.ASREnabled {
		status = "skipped"
	}
	peak := ev.PeakDB
	c := &store.Call{
		ReceiverID: rx.ID,
		Channel:    rx.Name,
		StartedAt:  store.FormatTime(ev.StartedAt),
		Duration:   ev.Duration,
		PeakDB:     &peak,
		Path:       path,
		Priority:   rx.ASRPriority,
		ASRStatus:  status,
	}
	if _, err := w.e.o.DB.InsertCall(c); err != nil {
		w.e.o.Logf("insert call: %v", err)
		return
	}
	w.e.o.Logf("call saved: rx=%s dur=%.1fs peak=%.1fdB %s", rx.Name, ev.Duration, ev.PeakDB, filepath.Base(path))
	if w.e.o.Notifier != nil {
		w.e.o.Notifier.OnCall(c, rx)
	}
	if status == "pending" {
		w.e.Wake()
	}
}

// snapshot returns the status fields of this receiver.
func (w *worker) snapshot() ReceiverStatus {
	w.mu.Lock()
	cs, le, lp := w.capState, w.lastErr, w.lastPacket
	w.mu.Unlock()
	st := ReceiverStatus{LevelDB: w.vox.LevelDB(), CallElapsed: w.vox.Elapsed(), Error: le}
	switch cs {
	case capture.StateError:
		st.State = StError
	case capture.StateWaiting, "":
		st.State = StWaiting
	case StStopped:
		st.State = StStopped
	default:
		st.State = StListening
		if w.vox.Recording() {
			st.State = StRecording
		}
	}
	if !lp.IsZero() {
		st.LastPacketAt = store.FormatTime(lp)
	}
	return st
}

func (w *worker) String() string { return fmt.Sprintf("worker(%s)", w.id) }
