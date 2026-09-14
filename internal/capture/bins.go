package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Binary describes where an external executable was found.
type Binary struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Found bool   `json:"found"`
	// Source is "runtime", "config" or "path".
	Source string `json:"source"`
}

// Resolve finds an executable following the fixed order: runtime/ folder,
// explicit config path, then PATH.
func Resolve(name, runtimeDir, configPath string) Binary {
	exe := name
	if runtime.GOOS == "windows" && !strings.HasSuffix(exe, ".exe") {
		exe += ".exe"
	}
	if runtimeDir != "" {
		candidates := []string{
			filepath.Join(runtimeDir, exe),
			filepath.Join(runtimeDir, name, exe),
			filepath.Join(runtimeDir, name, "bin", exe),
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return Binary{Name: name, Path: c, Found: true, Source: "runtime"}
			}
		}
	}
	if configPath != "" {
		if st, err := os.Stat(configPath); err == nil && !st.IsDir() {
			return Binary{Name: name, Path: configPath, Found: true, Source: "config"}
		}
	}
	if p, err := exec.LookPath(exe); err == nil {
		return Binary{Name: name, Path: p, Found: true, Source: "path"}
	}
	return Binary{Name: name, Found: false}
}

// SRTSupported reports whether the given ffmpeg lists the srt protocol.
func SRTSupported(ffmpeg string) bool {
	if ffmpeg == "" {
		return false
	}
	out, err := exec.Command(ffmpeg, "-hide_banner", "-protocols").CombinedOutput()
	if err != nil {
		return false
	}
	// The list contains one protocol per line; "srtp" must not match.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "srt" {
			return true
		}
	}
	return false
}
