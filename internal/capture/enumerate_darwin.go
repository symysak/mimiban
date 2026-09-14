//go:build darwin

package capture

import (
	"os/exec"
)

// FFmpegEnumerator lists devices using ffmpeg's avfoundation listing.
type FFmpegEnumerator struct{ FFmpeg string }

// Enumerate implements Enumerator.
func (e FFmpegEnumerator) Enumerate() ([]Device, error) {
	if e.FFmpeg == "" {
		return nil, errNoFFmpeg
	}
	// ffmpeg exits non-zero after listing; the list is on stderr.
	out, _ := exec.Command(e.FFmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	devs := ParseAVFoundation(string(out))
	if len(devs) == 0 {
		return nil, errEnumerateFailed
	}
	return devs, nil
}
