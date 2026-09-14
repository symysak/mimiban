//go:build windows

package capture

import (
	"os/exec"
)

// FFmpegEnumerator lists devices using ffmpeg's dshow listing.
type FFmpegEnumerator struct{ FFmpeg string }

// Enumerate implements Enumerator.
func (e FFmpegEnumerator) Enumerate() ([]Device, error) {
	if e.FFmpeg == "" {
		return nil, errNoFFmpeg
	}
	out, _ := exec.Command(e.FFmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
	devs := ParseDshow(string(out))
	if len(devs) == 0 {
		return nil, errEnumerateFailed
	}
	return devs, nil
}
