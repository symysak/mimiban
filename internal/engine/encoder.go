package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ffmpegEncoder pipes PCM into ffmpeg and writes an MP3.
type ffmpegEncoder struct{ ffmpeg string }

func (f *ffmpegEncoder) Encode(samples []int16, sampleRate int, bitrate, outPath string) error {
	if f.ffmpeg == "" {
		return errors.New("ffmpeg_not_found")
	}
	if bitrate == "" {
		bitrate = "48k"
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "1", "-i", "-",
		"-c:a", "libmp3lame", "-b:a", bitrate, "-f", "mp3", outPath}
	cmd := exec.Command(f.ffmpeg, args...)
	cmd.Stdin = bytes.NewReader(pcmBytes(samples))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outPath)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func pcmBytes(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

// WAVEncoder writes a plain WAV file (used by tests and as a fallback).
type WAVEncoder struct{}

// Encode implements Encoder.
func (WAVEncoder) Encode(samples []int16, sampleRate int, _ string, outPath string) error {
	data := pcmBytes(samples)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(data)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}
