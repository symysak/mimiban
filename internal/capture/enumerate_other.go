//go:build !darwin && !windows && !linux

package capture

// FFmpegEnumerator is a stub on unsupported platforms.
type FFmpegEnumerator struct{ FFmpeg string }

// Enumerate implements Enumerator.
func (e FFmpegEnumerator) Enumerate() ([]Device, error) { return nil, errEnumerateFailed }
