package capture

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// State values reported by FFmpegCapture.
const (
	StateWaiting   = "waiting"   // process alive, no data yet (stream/url)
	StateListening = "listening" // data flowing
	StateError     = "error"     // process died / stalled, will retry
	StateStopped   = "stopped"
)

// FFmpegCapture runs one ffmpeg process for one Source and restarts it when it
// dies or stalls. It implements Capture.
type FFmpegCapture struct {
	FFmpeg string
	Source Source
	// OnState is called on every state transition with the last stderr line.
	OnState func(state, lastErr string)
	// RetryInterval defaults to 5s; StallTimeout to 3s.
	RetryInterval time.Duration
	StallTimeout  time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Start launches the supervision loop and returns the block channel.
func (f *FFmpegCapture) Start(ctx context.Context) (<-chan Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
	if f.RetryInterval == 0 {
		f.RetryInterval = 5 * time.Second
	}
	if f.StallTimeout == 0 {
		f.StallTimeout = 3 * time.Second
	}
	cctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	f.done = make(chan struct{})
	out := make(chan Block, 8)
	go f.loop(cctx, out)
	return out, nil
}

// Stop terminates the process and the loop.
func (f *FFmpegCapture) Stop() {
	f.mu.Lock()
	c, d := f.cancel, f.done
	f.cancel = nil
	f.mu.Unlock()
	if c != nil {
		c()
	}
	if d != nil {
		<-d
	}
}

func (f *FFmpegCapture) setState(s, e string) {
	if f.OnState != nil {
		f.OnState(s, e)
	}
}

func (f *FFmpegCapture) loop(ctx context.Context, out chan<- Block) {
	defer close(out)
	defer close(f.done)
	for {
		lastErr := f.runOnce(ctx, out)
		if ctx.Err() != nil {
			f.setState(StateStopped, "")
			return
		}
		if lastErr == "" {
			lastErr = "ffmpeg_exited"
		}
		f.setState(StateError, lastErr)
		select {
		case <-ctx.Done():
			f.setState(StateStopped, "")
			return
		case <-time.After(f.RetryInterval):
		}
	}
}

// runOnce runs ffmpeg until it exits or stalls, returning the last stderr line.
func (f *FFmpegCapture) runOnce(ctx context.Context, out chan<- Block) string {
	args := InputArgs(f.Source)
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	cmd := exec.CommandContext(pctx, f.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err.Error()
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err.Error()
	}
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	f.setState(StateWaiting, "")

	var errMu sync.Mutex
	lastLine := ""
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l != "" {
				errMu.Lock()
				lastLine = l
				errMu.Unlock()
			}
		}
	}()

	ch := f.Source.OutChannels
	if ch == 0 {
		ch = 1
	}
	rate := f.Source.SampleRate
	if rate == 0 {
		rate = 16000
	}
	blockBytes := rate * BlockMS / 1000 * ch * 2

	type rd struct {
		buf []byte
		err error
	}
	reads := make(chan rd, 4)
	go func() {
		r := bufio.NewReaderSize(stdout, blockBytes*8)
		for {
			buf := make([]byte, blockBytes)
			_, err := io.ReadFull(r, buf)
			if err != nil {
				reads <- rd{nil, err}
				return
			}
			reads <- rd{buf, nil}
		}
	}()

	listening := false
	stall := time.NewTimer(f.StallTimeout)
	defer stall.Stop()
	// stream/url sources may legitimately wait a long time for a peer; do
	// not treat "no data yet" as a stall there.
	stallApplies := f.Source.Type == "device"
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Wait()
			return ""
		case r := <-reads:
			if r.err != nil {
				pcancel()
				_ = cmd.Wait()
				errMu.Lock()
				defer errMu.Unlock()
				return lastLine
			}
			if !listening {
				listening = true
				stallApplies = true
				f.setState(StateListening, "")
			}
			stall.Reset(f.StallTimeout)
			select {
			case out <- SplitBlock(r.buf, ch):
			case <-ctx.Done():
			}
		case <-stall.C:
			if !stallApplies {
				stall.Reset(f.StallTimeout)
				continue
			}
			pcancel()
			_ = cmd.Wait()
			errMu.Lock()
			defer errMu.Unlock()
			if lastLine == "" {
				return "stalled"
			}
			return lastLine
		}
	}
}
