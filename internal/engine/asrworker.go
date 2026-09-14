package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/store"
)

// asrLoop consumes pending calls one at a time.
func (e *Engine) asrLoop() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.asrWake:
		case <-time.After(3 * time.Second):
		}
		for e.ctx.Err() == nil {
			cfg := e.o.Config.Get()
			if !cfg.ASREnabled || e.o.Transcriber == nil || !e.asrAvailable(cfg) {
				// leave calls pending; they are picked up once whisper-server
				// and a model are present.
				break
			}
			c, err := e.o.DB.NextPending()
			if err != nil {
				e.o.Logf("asr next: %v", err)
				break
			}
			if c == nil {
				break
			}
			e.processOne(cfg, c)
		}
	}
}

// asrAvailable reports whether whisper-server and the model can be used. With
// a fake Transcriber (tests) and no Server it is always available.
func (e *Engine) asrAvailable(cfg config.Config) bool {
	if e.o.Server == nil {
		return true
	}
	o := e.serverOptions(cfg)
	return o.Binary != "" && o.ModelPath != ""
}

func (e *Engine) processOne(cfg config.Config, c *store.Call) {
	rx := cfg.FindReceiver(c.ReceiverID)
	if rx != nil && !rx.ASREnabled {
		_ = e.o.DB.SetStatus(c.ID, "skipped", nil)
		return
	}
	if _, err := os.Stat(c.Path); err != nil {
		msg := "audio_missing"
		_ = e.o.DB.SetStatus(c.ID, "error", &msg)
		return
	}
	_ = e.o.DB.SetStatus(c.ID, "running", nil)
	e.asrState.mu.Lock()
	e.asrState.runningID = c.ID
	e.asrState.mu.Unlock()
	defer func() {
		e.asrState.mu.Lock()
		e.asrState.runningID = 0
		e.asrState.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Minute)
	defer cancel()
	if e.o.Server != nil {
		e.o.Server.SetOptions(e.serverOptions(cfg))
		if err := e.o.Server.Ensure(ctx); err != nil {
			e.failASR(c.ID, err.Error())
			// back off so a missing binary doesn't spin
			select {
			case <-time.After(10 * time.Second):
			case <-e.ctx.Done():
			}
			return
		}
		e.o.Server.Touch()
	}
	// whisper-server accepts WAV only; decode the MP3 with ffmpeg first.
	audio, cleanup, err := e.toWAV(c.Path, cfg.SampleRate)
	if err != nil {
		e.failASR(c.ID, "wav_convert_failed: "+err.Error())
		return
	}
	defer cleanup()
	segs, err := e.o.Transcriber.Transcribe(ctx, audio, cfg.ASRLanguage, cfg.EffectivePrompt())
	if err != nil {
		e.failASR(c.ID, err.Error())
		return
	}
	phrases := append(append([]string{}, cfg.Hallucinations(cfg.ASRLanguage)...), asr.PromptPhrases(cfg.EffectivePrompt())...)
	res := asr.Filter(segs, phrases, cfg.NoSpeechThreshold)
	text := asr.ApplyDictionary(res.Text, cfg.Dictionary)
	model := cfg.ASRModel
	var note *string
	if res.Note != "" {
		n := res.Note
		note = &n
	}
	if err := e.o.DB.UpdateTranscript(c.ID, text, "done", &model, nil, note); err != nil {
		e.o.Logf("asr update: %v", err)
		return
	}
	e.asrState.mu.Lock()
	e.asrState.lastError = ""
	e.asrState.mu.Unlock()
	e.o.Logf("asr done: id=%d %s len=%d note=%s", c.ID, filepath.Base(c.Path), len(text), res.Note)
	if e.o.Notifier != nil && text != "" {
		c.Transcript = &text
		c.ASRStatus = "done"
		r := config.Receiver{ID: c.ReceiverID, Name: c.Channel, Color: "#3b82f6"}
		if rx != nil {
			r = *rx
		}
		e.o.Notifier.OnTranscript(c, r)
	}
}

func (e *Engine) failASR(id int64, msg string) {
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_ = e.o.DB.SetStatus(id, "error", &msg)
	e.asrState.mu.Lock()
	e.asrState.lastError = msg
	e.asrState.mu.Unlock()
	e.o.Logf("asr error: id=%d %s", id, msg)
}

// Retranscribe resets calls to pending. which: one / pending / all.
func (e *Engine) Retranscribe(which string, id int64) (int64, error) {
	n, err := e.o.DB.ResetStatus(which, id)
	e.Wake()
	return n, err
}

// ApplyDictionaryAll re-applies the dictionary to every stored transcript.
func (e *Engine) ApplyDictionaryAll() (int, error) {
	cfg := e.o.Config.Get()
	n := 0
	err := e.o.DB.AllTranscripts(func(id int64, text string) error {
		nt := asr.ApplyDictionary(text, cfg.Dictionary)
		if nt != text {
			n++
			return e.o.DB.SetTranscriptText(id, nt)
		}
		return nil
	})
	return n, err
}

// RestartASR restarts whisper-server.
func (e *Engine) RestartASR() error {
	if e.o.Server == nil {
		return nil
	}
	e.o.Server.SetOptions(e.serverOptions(e.o.Config.Get()))
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Minute)
	defer cancel()
	return e.o.Server.Restart(ctx)
}

// toWAV returns a 16 kHz mono WAV version of path. WAV inputs are passed
// through; MP3 is decoded with ffmpeg into a temp file that cleanup removes.
func (e *Engine) toWAV(path string, rate int) (string, func(), error) {
	if strings.EqualFold(filepath.Ext(path), ".wav") || e.o.Server == nil {
		// WAV already, or a fake Transcriber (tests) that does not care.
		return path, func() {}, nil
	}
	if e.ffmpeg.Path == "" {
		return "", nil, errors.New("ffmpeg_not_found")
	}
	tmp, err := os.CreateTemp("", "mimiban-asr-*.wav")
	if err != nil {
		return "", nil, err
	}
	tmp.Close()
	if rate == 0 {
		rate = 16000
	}
	cmd := exec.Command(e.ffmpeg.Path, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", path,
		"-ac", "1", "-ar", strconv.Itoa(rate), "-c:a", "pcm_s16le", "-f", "wav", tmp.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmp.Name())
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", nil, errors.New(msg)
	}
	return tmp.Name(), func() { os.Remove(tmp.Name()) }, nil
}
