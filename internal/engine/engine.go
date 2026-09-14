// Package engine ties capture, VOX, encoding, storage, ASR and notification
// together and owns all running receivers.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/capture"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/notify"
	"github.com/symysak/mimiban/internal/store"
)

// Receiver states (language independent codes).
const (
	StStopped   = "stopped"
	StWaiting   = "waiting"
	StListening = "listening"
	StRecording = "recording"
	StError     = "error"
)

// CaptureFactory builds a Capture for a source; tests inject fakes.
type CaptureFactory func(src capture.Source, onState func(state, lastErr string)) capture.Capture

// Options wire the engine's dependencies.
type Options struct {
	Config      *config.Store
	DB          *store.DB
	BaseDir     string // where recordings/ etc. live
	RuntimeDir  string
	Models      *asr.Models
	Enumerator  capture.Enumerator
	Factory     CaptureFactory // nil = real ffmpeg
	Transcriber asr.Transcriber
	Server      *asr.Server // may be nil when Transcriber is a fake
	Notifier    *notify.Sender
	Logf        func(string, ...any)
	// Encoder converts PCM to MP3; nil = ffmpeg. Tests use a fake.
	Encoder Encoder
	// Now is the clock (tests).
	Now func() time.Time
}

// Encoder writes mono 16 kHz s16le samples to an MP3 file.
type Encoder interface {
	Encode(samples []int16, sampleRate int, bitrate, outPath string) error
}

// Engine is the orchestrator.
type Engine struct {
	o        Options
	ffmpeg   capture.Binary
	whisper  capture.Binary
	srt      bool
	mu       sync.Mutex
	devices  []capture.Device
	captures map[string]*sharedCapture // key → shared ffmpeg
	workers  map[string]*worker        // receiver id → worker
	asrWake  chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	asrState asrState
	// fakeCapture is set when a CaptureFactory was injected (tests); the
	// ffmpeg binary is then not required to start receivers.
	fakeCapture bool
}

type asrState struct {
	mu        sync.Mutex
	runningID int64
	lastError string
}

// New creates the engine and resolves the external binaries.
func New(o Options) *Engine {
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	cfg := o.Config.Get()
	e := &Engine{
		o:        o,
		captures: map[string]*sharedCapture{},
		workers:  map[string]*worker{},
		asrWake:  make(chan struct{}, 1),
	}
	e.ffmpeg = capture.Resolve("ffmpeg", o.RuntimeDir, cfg.FFmpegPath)
	e.whisper = capture.Resolve("whisper-server", o.RuntimeDir, cfg.WhisperServerPath)
	if e.ffmpeg.Found {
		e.srt = capture.SRTSupported(e.ffmpeg.Path)
	}
	if e.o.Factory == nil {
		e.o.Factory = e.realFactory
	} else {
		e.fakeCapture = true
	}
	if e.o.Encoder == nil {
		e.o.Encoder = &ffmpegEncoder{ffmpeg: e.ffmpeg.Path}
	}
	if e.o.Enumerator == nil {
		e.o.Enumerator = capture.FFmpegEnumerator{FFmpeg: e.ffmpeg.Path}
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	return e
}

// Binaries returns detection results.
func (e *Engine) Binaries() (ffmpeg, whisper capture.Binary, srt bool) {
	return e.ffmpeg, e.whisper, e.srt
}

// FFmpegPath returns the resolved ffmpeg path ("" when missing).
func (e *Engine) FFmpegPath() string { return e.ffmpeg.Path }

// WhisperPath returns the resolved whisper-server path.
func (e *Engine) WhisperPath() string { return e.whisper.Path }

// Start performs the boot sequence: enumerate, resolve indices, auto-start,
// launch ASR worker and cleanup.
func (e *Engine) Start() {
	if n, _ := e.o.DB.ResetRunning(); n > 0 {
		e.o.Logf("asr: %d call(s) left running by a previous run were re-queued", n)
	}
	e.RefreshDevices()
	cfg := e.o.Config.Get()
	for _, r := range cfg.Receivers {
		if r.Enabled {
			if err := e.StartReceiver(r.ID); err != nil {
				e.o.Logf("receiver %s (%s) auto-start failed: %v", r.ID, r.Name, err)
			}
		}
	}
	e.wg.Add(2)
	go e.asrLoop()
	go e.cleanupLoop()
	e.Wake()
}

// Stop halts everything.
func (e *Engine) Stop() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.workers))
	for id := range e.workers {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		_ = e.StopReceiver(id)
	}
	e.cancel()
	e.wg.Wait()
	if e.o.Server != nil {
		e.o.Server.Stop()
	}
}

// Wake nudges the ASR worker.
func (e *Engine) Wake() {
	select {
	case e.asrWake <- struct{}{}:
	default:
	}
}

// RefreshDevices re-enumerates input devices and re-resolves indices by name.
func (e *Engine) RefreshDevices() []capture.Device {
	devs, err := e.o.Enumerator.Enumerate()
	if err != nil {
		e.o.Logf("device enumeration: %v", err)
	}
	e.mu.Lock()
	e.devices = devs
	e.mu.Unlock()
	_ = e.o.Config.Update(func(c *config.Config) error {
		for i := range c.Receivers {
			r := &c.Receivers[i]
			if r.SourceType == "device" && r.DeviceName != "" {
				r.DeviceIndex = capture.ResolveIndex(devs, r.DeviceName, r.DeviceIndex)
			}
		}
		return nil
	})
	return devs
}

// Devices returns the cached device list annotated with usage.
func (e *Engine) Devices() []capture.Device {
	e.mu.Lock()
	devs := append([]capture.Device(nil), e.devices...)
	e.mu.Unlock()
	cfg := e.o.Config.Get()
	for i := range devs {
		devs[i].InUseBy = usageOf(cfg.Receivers, devs[i].Name, "")
	}
	return devs
}

// usageOf summarises which channels of device name are taken, ignoring
// receiver exceptID.
func usageOf(rs []config.Receiver, name, exceptID string) string {
	var parts []string
	for _, r := range rs {
		if r.SourceType != "device" || r.DeviceName != name || r.ID == exceptID {
			continue
		}
		parts = append(parts, r.ChannelSelect)
	}
	sort.Strings(parts)
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// ValidateReceiver enforces the device/port/url uniqueness rules. exceptID is
// the receiver being edited.
func ValidateReceiver(cfg config.Config, r config.Receiver, exceptID string, httpPort int) error {
	if r.Name == "" {
		return errors.New("name_required")
	}
	switch r.ChannelSelect {
	case "mono", "left", "right":
	default:
		return errors.New("invalid_channel_select")
	}
	switch r.SourceType {
	case "device":
		if r.DeviceName == "" {
			return errors.New("device_required")
		}
		for _, o := range cfg.Receivers {
			if o.ID == exceptID || o.SourceType != "device" || o.DeviceName != r.DeviceName {
				continue
			}
			if o.ChannelSelect == "mono" || r.ChannelSelect == "mono" {
				return errors.New("device_channel_conflict")
			}
			if o.ChannelSelect == r.ChannelSelect {
				return errors.New("device_channel_duplicate")
			}
		}
	case "stream":
		if r.ListenPort <= 0 || r.ListenPort > 65535 {
			return errors.New("listen_port_required")
		}
		if r.ListenPort == httpPort {
			return errors.New("listen_port_conflict")
		}
		for _, o := range cfg.Receivers {
			if o.ID != exceptID && o.SourceType == "stream" && o.ListenPort == r.ListenPort {
				return errors.New("listen_port_conflict")
			}
		}
	case "url":
		if r.InputURL == "" {
			return errors.New("input_url_required")
		}
		for _, o := range cfg.Receivers {
			if o.ID != exceptID && o.SourceType == "url" && o.InputURL == r.InputURL {
				return errors.New("input_url_conflict")
			}
		}
	default:
		return errors.New("invalid_source_type")
	}
	if r.MinCallSec < 0 || r.SilenceSec < 0 || r.PrebufferSec < 0 || r.MaxCallSec <= 0 {
		return errors.New("invalid_vox_params")
	}
	return nil
}

// RecordDir returns the absolute recordings directory.
func (e *Engine) RecordDir() string {
	cfg := e.o.Config.Get()
	d := cfg.RecordDir
	if !filepath.IsAbs(d) {
		d = filepath.Join(e.o.BaseDir, d)
	}
	return d
}

// ApplyConfig is called after the global config changed: pushes ASR options
// and VOX params to running components.
func (e *Engine) ApplyConfig() {
	cfg := e.o.Config.Get()
	if e.o.Server != nil {
		e.o.Server.SetOptions(e.serverOptions(cfg))
	}
	e.mu.Lock()
	for id, w := range e.workers {
		if r := cfg.FindReceiver(id); r != nil {
			w.setParams(*r)
		}
	}
	e.mu.Unlock()
	e.Wake()
}

func (e *Engine) serverOptions(cfg config.Config) asr.ServerOptions {
	o := asr.ServerOptions{
		Binary:     e.whisper.Path,
		Language:   cfg.ASRLanguage,
		Threads:    cfg.ASRThreads,
		IdleUnload: time.Duration(cfg.ASRIdleUnloadMin) * time.Minute,
	}
	if cfg.WhisperServerPath != "" && !e.whisper.Found {
		if _, err := os.Stat(cfg.WhisperServerPath); err == nil {
			o.Binary = cfg.WhisperServerPath
		}
	}
	if e.o.Models != nil {
		o.ModelPath = e.o.Models.Path(cfg.ASRModel)
		if cfg.ASRVAD {
			o.VADModel = e.o.Models.Path("silero-vad")
		}
	}
	return o
}

// realFactory builds an ffmpeg backed capture.
func (e *Engine) realFactory(src capture.Source, onState func(string, string)) capture.Capture {
	src.OS = runtime.GOOS
	src.SRTSupported = e.srt
	return &capture.FFmpegCapture{FFmpeg: e.ffmpeg.Path, Source: src, OnState: onState}
}

func (e *Engine) errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
