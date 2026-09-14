// Package capture starts ffmpeg child processes and turns their raw PCM output
// into fixed-size blocks. OS specific parts are limited to device enumeration
// and the ffmpeg input flags.
package capture

import (
	"context"
	"fmt"
	"strconv"
)

// Block is one chunk of decoded audio, already split per channel.
// Channels[i] holds the samples of input channel i (s16le → int16).
type Block struct {
	Channels [][]int16
}

// Capture produces Blocks until Stop is called or ctx ends.
type Capture interface {
	Start(ctx context.Context) (<-chan Block, error)
	Stop()
}

// Device is one enumerated audio input device.
type Device struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	HostAPI   string `json:"hostapi"`
	Channels  int    `json:"channels"`
	IsDefault bool   `json:"is_default"`
	// InUseBy is filled in by the engine: "" / "mono" / "left" / "right" / "left,right".
	InUseBy string `json:"in_use_by"`
}

// Source describes what ffmpeg should open.
type Source struct {
	Type          string // device / stream / url
	DeviceName    string
	DeviceIndex   int
	ListenPort    int
	Passphrase    string
	InputURL      string
	InputFormat   string
	InputRate     int
	InputChannels int
	// OutChannels is 1 or 2 (2 when L/R split is needed).
	OutChannels int
	SampleRate  int
	// OS overrides runtime.GOOS for tests.
	OS string
	// SRTSupported controls the stream input scheme (srt vs udp fallback).
	SRTSupported bool
}

// BlockMS is the block length in milliseconds.
const BlockMS = 50

// InputArgs returns the ffmpeg arguments for opening the source and writing
// s16le PCM at SampleRate with OutChannels channels to stdout.
func InputArgs(s Source) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch s.Type {
	case "stream":
		args = append(args, "-fflags", "nobuffer", "-flags", "low_delay")
		if s.SRTSupported {
			u := fmt.Sprintf("srt://0.0.0.0:%d?mode=listener&latency=200000", s.ListenPort)
			if s.Passphrase != "" {
				u += "&passphrase=" + s.Passphrase
			}
			args = append(args, "-i", u)
		} else {
			args = append(args, "-i", fmt.Sprintf("udp://0.0.0.0:%d?fifo_size=1000000&overrun_nonfatal=1", s.ListenPort))
		}
	case "url":
		args = append(args, "-fflags", "nobuffer", "-flags", "low_delay")
		if s.InputFormat != "" {
			args = append(args, "-f", s.InputFormat)
			if s.InputRate > 0 {
				args = append(args, "-ar", strconv.Itoa(s.InputRate))
			}
			if s.InputChannels > 0 {
				args = append(args, "-ac", strconv.Itoa(s.InputChannels))
			}
		}
		args = append(args, "-i", s.InputURL)
	default: // device
		args = append(args, deviceInputArgs(s)...)
	}
	rate := s.SampleRate
	if rate == 0 {
		rate = 16000
	}
	oc := s.OutChannels
	if oc == 0 {
		oc = 1
	}
	args = append(args, "-ac", strconv.Itoa(oc), "-ar", strconv.Itoa(rate), "-f", "s16le", "-")
	return args
}

// deviceInputArgs returns the OS specific "-f X -i Y" part for a local device.
func deviceInputArgs(s Source) []string {
	switch s.OS {
	case "windows":
		return []string{"-fflags", "nobuffer", "-flags", "low_delay", "-f", "dshow",
			"-audio_buffer_size", "50", "-i", "audio=" + s.DeviceName}
	case "darwin":
		// avfoundation takes ":<audio index>" (or ":<name>"). Index is stable
		// within one enumeration; we re-resolve by name at start.
		return []string{"-fflags", "nobuffer", "-flags", "low_delay", "-f", "avfoundation",
			"-i", ":" + strconv.Itoa(s.DeviceIndex)}
	default: // linux
		name := s.DeviceName
		if name == "" {
			name = "default"
		}
		fmtName := "alsa"
		if isPulseName(name) {
			fmtName = "pulse"
		}
		return []string{"-fflags", "nobuffer", "-flags", "low_delay", "-f", fmtName, "-i", name}
	}
}

func isPulseName(n string) bool {
	// PulseAudio / PipeWire source names look like "alsa_input.usb-...", ALSA
	// devices look like "hw:1,0" / "plughw:CARD=Device,DEV=0" / "default".
	if len(n) >= 4 && (n[:4] == "hw:" || n[:4] == "plug") {
		return false
	}
	for _, p := range []string{"alsa_input.", "alsa_output.", "pulse", "@DEFAULT"} {
		if len(n) >= len(p) && n[:len(p)] == p {
			return true
		}
	}
	return false
}

// SplitBlock converts raw interleaved s16le bytes into a Block.
func SplitBlock(raw []byte, channels int) Block {
	frames := len(raw) / 2 / channels
	b := Block{Channels: make([][]int16, channels)}
	for c := range b.Channels {
		b.Channels[c] = make([]int16, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			o := (i*channels + c) * 2
			b.Channels[c][i] = int16(uint16(raw[o]) | uint16(raw[o+1])<<8)
		}
	}
	return b
}

// Mono returns the average of all channels (used for channel_select=mono when
// the capture had to be opened with 2 channels for another receiver).
func Mono(b Block) []int16 {
	if len(b.Channels) == 1 {
		return b.Channels[0]
	}
	n := len(b.Channels[0])
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		var sum int
		for _, ch := range b.Channels {
			sum += int(ch[i])
		}
		out[i] = int16(sum / len(b.Channels))
	}
	return out
}
