package agent

import (
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	base := Options{Device: "USB Audio Device", DeviceIndex: 2, To: "100.1.2.3:5004", SRTSupported: true}
	cases := []struct {
		name string
		mod  func(*Options)
		want []string
		not  []string
	}{
		{"win-1ch", func(o *Options) { o.OS = "windows" }, []string{"-f dshow", "-i audio=USB Audio Device", "-ac 1", "-b:a 32k", "srt://100.1.2.3:5004?mode=caller&latency=200000"}, []string{"passphrase"}},
		{"mac-2ch", func(o *Options) { o.OS = "darwin"; o.Channels = 2 }, []string{"-f avfoundation -i :2", "-ac 2", "-b:a 48k"}, nil},
		{"linux-pass", func(o *Options) { o.OS = "linux"; o.Passphrase = "abcdefghij" }, []string{"-f alsa -i USB Audio Device", "&passphrase=abcdefghij"}, nil},
		{"bitrate", func(o *Options) { o.OS = "linux"; o.Bitrate = "24k" }, []string{"-b:a 24k"}, nil},
		{"udp-fallback", func(o *Options) { o.OS = "linux"; o.SRTSupported = false }, []string{"udp://100.1.2.3:5004"}, []string{"srt://"}},
		{"input-url", func(o *Options) {
			o.OS = "linux"
			o.InputURL = "udp://127.0.0.1:6001"
			o.InputFormat = "s16le"
			o.InputRate = 16000
			o.InputChannels = 1
		},
			[]string{"-f s16le -ar 16000 -ac 1 -i udp://127.0.0.1:6001"}, []string{"alsa", "USB Audio"}},
	}
	for _, c := range cases {
		o := base
		c.mod(&o)
		got := strings.Join(Args(o), " ")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: %q should not contain %q", c.name, got, n)
			}
		}
		if !strings.Contains(got, "-c:a libopus") || !strings.Contains(got, "-application voip -f mpegts") {
			t.Errorf("%s: opus/mpegts missing: %q", c.name, got)
		}
	}
}
