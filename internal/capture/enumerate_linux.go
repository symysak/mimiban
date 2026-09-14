//go:build linux

package capture

import (
	"os/exec"
)

// FFmpegEnumerator lists devices using pactl (PulseAudio/PipeWire) and falls
// back to `arecord -l` (ALSA).
type FFmpegEnumerator struct{ FFmpeg string }

// Enumerate implements Enumerator.
func (e FFmpegEnumerator) Enumerate() ([]Device, error) {
	var devs []Device
	if out, err := exec.Command("pactl", "list", "short", "sources").Output(); err == nil {
		devs = append(devs, ParsePactl(string(out))...)
	}
	if out, err := exec.Command("arecord", "-l").Output(); err == nil {
		alsa := ParseArecordL(string(out))
		for i := range alsa {
			alsa[i].Index = len(devs) + i
		}
		devs = append(devs, alsa...)
	}
	if len(devs) == 0 {
		return nil, errEnumerateFailed
	}
	return devs, nil
}
