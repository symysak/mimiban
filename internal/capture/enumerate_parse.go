package capture

import (
	"regexp"
	"strconv"
	"strings"
)

// Enumerator lists the audio input devices of this machine.
type Enumerator interface {
	Enumerate() ([]Device, error)
}

var (
	reAVF   = regexp.MustCompile(`\[(\d+)\]\s+(.+?)\s*$`)
	reDshow = regexp.MustCompile(`"([^"]+)"\s+\(audio\)`)
	reCard  = regexp.MustCompile(`^card (\d+): ([^\[]+) \[([^\]]+)\], device (\d+): ([^\[]+) \[([^\]]+)\]`)
)

// ParseAVFoundation extracts audio devices from the stderr of
// `ffmpeg -f avfoundation -list_devices true -i ""`.
func ParseAVFoundation(out string) []Device {
	var devs []Device
	inAudio := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "AVFoundation audio devices") {
			inAudio = true
			continue
		}
		if strings.Contains(line, "AVFoundation video devices") {
			inAudio = false
			continue
		}
		if !inAudio {
			continue
		}
		i := strings.Index(line, "]")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+1:])
		m := reAVF.FindStringSubmatch(rest)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		devs = append(devs, Device{Index: idx, Name: m[2], HostAPI: "avfoundation", Channels: 2})
	}
	return devs
}

// ParseDshow extracts audio devices from `ffmpeg -list_devices true -f dshow -i dummy`.
func ParseDshow(out string) []Device {
	var devs []Device
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := reDshow.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		devs = append(devs, Device{Index: len(devs), Name: m[1], HostAPI: "dshow", Channels: 2})
	}
	return devs
}

// ParsePactl extracts sources from `pactl list short sources`.
func ParsePactl(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 || strings.Contains(f[1], ".monitor") {
			continue
		}
		idx, _ := strconv.Atoi(f[0])
		d := Device{Index: idx, Name: f[1], HostAPI: "pulse", Channels: 2}
		if len(f) >= 4 && strings.Contains(f[3], "1ch") {
			d.Channels = 1
		}
		devs = append(devs, d)
	}
	return devs
}

// ParseArecordL extracts capture devices from `arecord -l` (hw:X,Y form).
func ParseArecordL(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		m := reCard.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name := "plughw:" + m[1] + "," + m[4]
		devs = append(devs, Device{Index: len(devs), Name: name, HostAPI: "alsa (" + strings.TrimSpace(m[3]) + " / " + strings.TrimSpace(m[6]) + ")", Channels: 2})
	}
	return devs
}

// ResolveIndex finds the device index for name; falls back to fallbackIdx.
func ResolveIndex(devs []Device, name string, fallbackIdx int) int {
	for _, d := range devs {
		if d.Name == name {
			return d.Index
		}
	}
	return fallbackIdx
}
