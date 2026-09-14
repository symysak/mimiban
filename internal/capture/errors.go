package capture

import "errors"

var (
	errNoFFmpeg        = errors.New("ffmpeg_not_found")
	errEnumerateFailed = errors.New("device_enumerate_failed")
)
