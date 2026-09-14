package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/symysak/mimiban/internal/capture"
	"github.com/symysak/mimiban/internal/config"
)

// sharedCapture is one ffmpeg process feeding one or more receivers.
type sharedCapture struct {
	key      string
	src      capture.Source
	cap      capture.Capture
	mu       sync.Mutex
	subs     map[string]*worker // receiver id → worker
	state    string
	lastErr  string
	lastData time.Time
	stop     chan struct{}
	done     chan struct{}
}

// captureKey identifies the physical source of a receiver.
func captureKey(r config.Receiver) string {
	switch r.SourceType {
	case "stream":
		return fmt.Sprintf("stream:%d", r.ListenPort)
	case "url":
		return "url:" + r.InputURL
	default:
		return "device:" + r.DeviceName
	}
}

// sourceFor builds the ffmpeg Source for a receiver. Stereo (2 output
// channels) is used whenever left/right selection is involved.
func sourceFor(r config.Receiver, sampleRate int, stereo bool) capture.Source {
	s := capture.Source{
		Type:          r.SourceType,
		DeviceName:    r.DeviceName,
		DeviceIndex:   r.DeviceIndex,
		ListenPort:    r.ListenPort,
		Passphrase:    r.StreamPassphrase,
		InputURL:      r.InputURL,
		InputFormat:   r.InputFormat,
		InputRate:     r.InputRate,
		InputChannels: r.InputChannels,
		OutChannels:   1,
		SampleRate:    sampleRate,
	}
	if stereo {
		s.OutChannels = 2
	}
	return s
}

// StartReceiver starts (or joins) the capture for receiver id.
func (e *Engine) StartReceiver(id string) error {
	cfg := e.o.Config.Get()
	r := cfg.FindReceiver(id)
	if r == nil {
		return e.errorf("receiver_not_found")
	}
	if !e.ffmpeg.Found && !e.fakeCapture {
		return e.errorf("ffmpeg_not_found")
	}
	e.mu.Lock()
	if _, ok := e.workers[id]; ok {
		e.mu.Unlock()
		return nil
	}
	w := newWorker(e, *r, cfg.SampleRate)
	e.workers[id] = w
	key := captureKey(*r)
	sc, ok := e.captures[key]
	stereo := r.ChannelSelect != "mono"
	if ok && stereo && sc.src.OutChannels != 2 {
		// existing mono capture cannot serve L/R — restart it as stereo
		e.mu.Unlock()
		e.restartShared(sc, true)
		e.mu.Lock()
		sc = e.captures[key]
		ok = sc != nil
	}
	if !ok {
		sc = &sharedCapture{key: key, src: sourceFor(*r, cfg.SampleRate, stereo), subs: map[string]*worker{}, state: StWaiting}
		e.captures[key] = sc
		e.mu.Unlock()
		e.launchShared(sc)
		e.mu.Lock()
	}
	sc.mu.Lock()
	sc.subs[id] = w
	w.setCaptureState(sc.state, sc.lastErr)
	sc.mu.Unlock()
	w.shared = sc
	e.mu.Unlock()
	e.o.Logf("receiver started: %s (%s) via %s", r.Name, id, key)
	return nil
}

func (e *Engine) launchShared(sc *sharedCapture) {
	sc.stop = make(chan struct{})
	sc.done = make(chan struct{})
	sc.cap = e.o.Factory(sc.src, func(state, lastErr string) {
		sc.mu.Lock()
		sc.state = state
		sc.lastErr = lastErr
		if state == capture.StateListening {
			sc.lastData = e.o.Now()
		}
		subs := make([]*worker, 0, len(sc.subs))
		for _, w := range sc.subs {
			subs = append(subs, w)
		}
		sc.mu.Unlock()
		for _, w := range subs {
			w.setCaptureState(state, lastErr)
			if state == capture.StateError {
				w.flush()
			}
		}
	})
	blocks, err := sc.cap.Start(e.ctx)
	if err != nil {
		sc.mu.Lock()
		sc.state = StError
		sc.lastErr = err.Error()
		sc.mu.Unlock()
		close(sc.done)
		return
	}
	go func() {
		defer close(sc.done)
		for {
			select {
			case <-sc.stop:
				return
			case b, ok := <-blocks:
				if !ok {
					return
				}
				now := e.o.Now()
				sc.mu.Lock()
				sc.lastData = now
				subs := make([]*worker, 0, len(sc.subs))
				for _, w := range sc.subs {
					subs = append(subs, w)
				}
				sc.mu.Unlock()
				for _, w := range subs {
					w.feed(b, now)
				}
			}
		}
	}()
}

func (e *Engine) stopShared(sc *sharedCapture) {
	if sc.cap != nil {
		sc.cap.Stop()
	}
	if sc.stop != nil {
		select {
		case <-sc.stop:
		default:
			close(sc.stop)
		}
	}
	if sc.done != nil {
		<-sc.done
	}
}

// restartShared replaces a running capture with a new channel layout.
func (e *Engine) restartShared(sc *sharedCapture, stereo bool) {
	e.stopShared(sc)
	e.mu.Lock()
	src := sc.src
	if stereo {
		src.OutChannels = 2
	} else {
		src.OutChannels = 1
	}
	nsc := &sharedCapture{key: sc.key, src: src, subs: sc.subs, state: StWaiting}
	for _, w := range nsc.subs {
		w.shared = nsc
	}
	e.captures[sc.key] = nsc
	e.mu.Unlock()
	e.launchShared(nsc)
}

// StopReceiver stops one receiver and closes the ffmpeg when unused.
func (e *Engine) StopReceiver(id string) error {
	e.mu.Lock()
	w, ok := e.workers[id]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	delete(e.workers, id)
	sc := w.shared
	var closeIt bool
	if sc != nil {
		sc.mu.Lock()
		delete(sc.subs, id)
		closeIt = len(sc.subs) == 0
		sc.mu.Unlock()
		if closeIt {
			delete(e.captures, sc.key)
		}
	}
	e.mu.Unlock()
	w.flush()
	w.setCaptureState(StStopped, "")
	if closeIt {
		e.stopShared(sc)
	}
	e.o.Logf("receiver stopped: %s", id)
	return nil
}

// RestartReceiver stops then starts (after an edit changed the source).
func (e *Engine) RestartReceiver(id string) error {
	_ = e.StopReceiver(id)
	return e.StartReceiver(id)
}

// Running reports whether a receiver has a worker.
func (e *Engine) Running(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.workers[id]
	return ok
}

// StartAll / StopAll operate on every configured receiver.
func (e *Engine) StartAll() {
	for _, r := range e.o.Config.Get().Receivers {
		if err := e.StartReceiver(r.ID); err != nil {
			e.o.Logf("start %s: %v", r.ID, err)
		}
	}
}

// StopAll stops every receiver.
func (e *Engine) StopAll() {
	for _, r := range e.o.Config.Get().Receivers {
		_ = e.StopReceiver(r.ID)
	}
}
