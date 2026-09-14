package capture

import (
	"strings"
	"testing"
)

func join(a []string) string { return strings.Join(a, " ") }

func TestInputArgsDevice(t *testing.T) {
	cases := []struct {
		os   string
		want string
	}{
		{"windows", `-f dshow -audio_buffer_size 50 -i audio=USB Audio Device`},
		{"darwin", `-f avfoundation -i :3`},
		{"linux", `-f alsa -i USB Audio Device`},
	}
	for _, c := range cases {
		s := Source{Type: "device", DeviceName: "USB Audio Device", DeviceIndex: 3, OS: c.os, OutChannels: 2, SampleRate: 16000}
		got := join(InputArgs(s))
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not contain %q", c.os, got, c.want)
		}
		if !strings.HasSuffix(got, "-ac 2 -ar 16000 -f s16le -") {
			t.Errorf("%s: bad output tail: %q", c.os, got)
		}
	}
	// pulse name on linux
	got := join(InputArgs(Source{Type: "device", DeviceName: "alsa_input.usb-foo", OS: "linux"}))
	if !strings.Contains(got, "-f pulse -i alsa_input.usb-foo") {
		t.Errorf("pulse: %q", got)
	}
	if !strings.HasSuffix(got, "-ac 1 -ar 16000 -f s16le -") {
		t.Errorf("mono default: %q", got)
	}
}

func TestInputArgsStream(t *testing.T) {
	got := join(InputArgs(Source{Type: "stream", ListenPort: 5004, SRTSupported: true, Passphrase: "secretpass"}))
	if !strings.Contains(got, "-i srt://0.0.0.0:5004?mode=listener&latency=200000&passphrase=secretpass") {
		t.Errorf("srt: %q", got)
	}
	got = join(InputArgs(Source{Type: "stream", ListenPort: 5004, SRTSupported: false}))
	if !strings.Contains(got, "-i udp://0.0.0.0:5004?") {
		t.Errorf("udp fallback: %q", got)
	}
}

func TestInputArgsURL(t *testing.T) {
	got := join(InputArgs(Source{Type: "url", InputURL: "udp://127.0.0.1:6001", InputFormat: "s16le", InputRate: 16000, InputChannels: 1}))
	if !strings.Contains(got, "-f s16le -ar 16000 -ac 1 -i udp://127.0.0.1:6001") {
		t.Errorf("raw url: %q", got)
	}
	got = join(InputArgs(Source{Type: "url", InputURL: "http://host:8000/ch1.mp3"}))
	if strings.Contains(got, "-f s16le -ar") || !strings.Contains(got, "-i http://host:8000/ch1.mp3") {
		t.Errorf("auto url: %q", got)
	}
}

func TestSplitBlockAndMono(t *testing.T) {
	// two frames, stereo: L=1000,R=-1000 ; L=2000,R=0
	raw := []byte{0xE8, 0x03, 0x18, 0xFC, 0xD0, 0x07, 0x00, 0x00}
	b := SplitBlock(raw, 2)
	if len(b.Channels) != 2 || b.Channels[0][0] != 1000 || b.Channels[1][0] != -1000 || b.Channels[0][1] != 2000 {
		t.Fatalf("split: %+v", b)
	}
	m := Mono(b)
	if m[0] != 0 || m[1] != 1000 {
		t.Fatalf("mono: %v", m)
	}
}

func TestParsers(t *testing.T) {
	avf := `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] FaceTime HD Camera
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] MacBook Pro Microphone
[AVFoundation indev @ 0x1] [1] USB Audio Device
`
	d := ParseAVFoundation(avf)
	if len(d) != 2 || d[1].Name != "USB Audio Device" || d[1].Index != 1 {
		t.Fatalf("avf: %+v", d)
	}
	dshow := `[dshow @ 0x1] "Microphone (USB Audio Device)" (audio)
[dshow @ 0x1]   Alternative name "@device_cm_{33D9A762}\wave_{F0}"
[dshow @ 0x1] "Integrated Camera" (video)
`
	d = ParseDshow(dshow)
	if len(d) != 1 || d[0].Name != "Microphone (USB Audio Device)" {
		t.Fatalf("dshow: %+v", d)
	}
	pactl := "0\talsa_output.pci.monitor\tmodule\ts16le 2ch 44100Hz\tSUSPENDED\n1\talsa_input.usb-foo\tmodule\ts16le 1ch 48000Hz\tRUNNING\n"
	d = ParsePactl(pactl)
	if len(d) != 1 || d[0].Name != "alsa_input.usb-foo" || d[0].Channels != 1 {
		t.Fatalf("pactl: %+v", d)
	}
	arec := "card 1: Device [USB Audio Device], device 0: USB Audio [USB Audio]\n"
	d = ParseArecordL(arec)
	if len(d) != 1 || d[0].Name != "plughw:1,0" {
		t.Fatalf("arecord: %+v", d)
	}
	if ResolveIndex(ParseAVFoundation(avf), "USB Audio Device", 9) != 1 || ResolveIndex(nil, "x", 9) != 9 {
		t.Fatal("resolve index")
	}
}
