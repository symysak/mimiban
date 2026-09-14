// Package agent implements `mimiban agent`: capture a device (or relay a URL)
// and stream it to the main instance over SRT with Opus, forever.
package agent

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/symysak/mimiban/internal/capture"
)

// Options are the agent flags.
type Options struct {
	Device        string
	DeviceIndex   int
	Channels      int
	To            string
	Bitrate       string
	Passphrase    string
	InputURL      string
	InputFormat   string
	InputRate     int
	InputChannels int
	OS            string
	ListDevices   bool
	FFmpeg        string
	SRTSupported  bool
}

// Args builds the ffmpeg command line for the agent.
func Args(o Options) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-fflags", "nobuffer", "-flags", "low_delay"}
	if o.InputURL != "" {
		if o.InputFormat != "" {
			args = append(args, "-f", o.InputFormat)
			if o.InputRate > 0 {
				args = append(args, "-ar", strconv.Itoa(o.InputRate))
			}
			if o.InputChannels > 0 {
				args = append(args, "-ac", strconv.Itoa(o.InputChannels))
			}
		}
		args = append(args, "-i", o.InputURL)
	} else {
		switch o.OS {
		case "windows":
			args = append(args, "-f", "dshow", "-audio_buffer_size", "50", "-i", "audio="+o.Device)
		case "darwin":
			args = append(args, "-f", "avfoundation", "-i", ":"+strconv.Itoa(o.DeviceIndex))
		default:
			f := "alsa"
			if strings.HasPrefix(o.Device, "alsa_input.") || strings.HasPrefix(o.Device, "pulse") {
				f = "pulse"
			}
			dev := o.Device
			if dev == "" {
				dev = "default"
			}
			args = append(args, "-f", f, "-i", dev)
		}
	}
	ch := o.Channels
	if ch != 2 {
		ch = 1
	}
	br := o.Bitrate
	if br == "" {
		if ch == 2 {
			br = "48k"
		} else {
			br = "32k"
		}
	}
	args = append(args, "-ac", strconv.Itoa(ch), "-ar", "16000", "-c:a", "libopus", "-b:a", br, "-application", "voip", "-f", "mpegts")
	if o.SRTSupported {
		u := "srt://" + o.To + "?mode=caller&latency=200000"
		if o.Passphrase != "" {
			u += "&passphrase=" + o.Passphrase
		}
		args = append(args, u)
	} else {
		args = append(args, "udp://"+o.To+"?pkt_size=1316")
	}
	return args
}

// Run parses args and runs the agent until interrupted.
func Run(argv []string, runtimeDir string) int {
	fs := flag.NewFlagSet("mimiban agent", flag.ContinueOnError)
	var o Options
	fs.StringVar(&o.Device, "device", "", "audio input device name (see --list-devices)")
	fs.IntVar(&o.Channels, "channels", 1, "channels to send (1 or 2)")
	fs.StringVar(&o.To, "to", "", "destination host:port of the Mimiban stream receiver")
	fs.StringVar(&o.Bitrate, "bitrate", "", "opus bitrate (default 32k for 1ch, 48k for 2ch)")
	fs.StringVar(&o.Passphrase, "passphrase", "", "SRT passphrase (must match the receiver)")
	fs.StringVar(&o.InputURL, "input-url", "", "relay this ffmpeg input URL instead of a device")
	fs.StringVar(&o.InputFormat, "input-format", "", "raw input format for --input-url (e.g. s16le)")
	fs.IntVar(&o.InputRate, "input-rate", 0, "raw input sample rate")
	fs.IntVar(&o.InputChannels, "input-channels", 0, "raw input channels")
	fs.BoolVar(&o.ListDevices, "list-devices", false, "list audio devices and exit")
	fs.StringVar(&o.FFmpeg, "ffmpeg", "", "explicit ffmpeg path")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	bin := capture.Resolve("ffmpeg", runtimeDir, o.FFmpeg)
	if !bin.Found {
		fmt.Println("error: ffmpeg not found (runtime/, --ffmpeg, PATH)")
		return 1
	}
	o.FFmpeg = bin.Path
	o.OS = runtime.GOOS
	o.SRTSupported = capture.SRTSupported(bin.Path)
	enum := capture.FFmpegEnumerator{FFmpeg: bin.Path}
	if o.ListDevices {
		devs, err := enum.Enumerate()
		if err != nil {
			fmt.Println("error:", err)
			return 1
		}
		for _, d := range devs {
			fmt.Printf("[%d] %s (%s)\n", d.Index, d.Name, d.HostAPI)
		}
		return 0
	}
	if o.To == "" || (o.Device == "" && o.InputURL == "") {
		fmt.Println("usage: mimiban agent --device \"<name>\" --to <host>:<port> [--channels 2] [--passphrase x]")
		fmt.Println("       mimiban agent --input-url udp://127.0.0.1:6001 --input-format s16le --input-rate 16000 --to <host>:<port>")
		fmt.Println("       mimiban agent --list-devices")
		return 2
	}
	if !o.SRTSupported {
		fmt.Println("warn: this ffmpeg has no srt protocol; falling back to plain UDP (receiver must use udp too)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("mimiban agent: ffmpeg=%s to=%s device=%q url=%q channels=%d\n", bin.Path, o.To, o.Device, o.InputURL, o.Channels)
	for ctx.Err() == nil {
		if o.InputURL == "" && o.OS == "darwin" {
			if devs, err := enum.Enumerate(); err == nil {
				o.DeviceIndex = capture.ResolveIndex(devs, o.Device, o.DeviceIndex)
			}
		}
		last, err := runOnce(ctx, o)
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("%s ffmpeg exited: %v %s\n", time.Now().Format(time.RFC3339), err, last)
		fmt.Println("restarting in 5s")
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	fmt.Println("mimiban agent: stopped")
	return 0
}

func runOnce(ctx context.Context, o Options) (string, error) {
	args := Args(o)
	cmd := exec.CommandContext(ctx, o.FFmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return "", err
	}
	fmt.Printf("%s ffmpeg started (pid %d), connecting to %s\n", time.Now().Format(time.RFC3339), cmd.Process.Pid, o.To)
	last := ""
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" {
			last = l
			fmt.Println("ffmpeg:", l)
		}
	}
	return last, cmd.Wait()
}
