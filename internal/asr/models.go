package asr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// ModelInfo describes one downloadable ggml model.
type ModelInfo struct {
	Name     string    `json:"name"` // key used in config.asr_model
	File     string    `json:"file"`
	Repo     string    `json:"repo"`
	Kind     string    `json:"kind"` // whisper / vad
	Label    string    `json:"label"`
	SizeMB   int       `json:"size_mb"`
	Present  bool      `json:"present"`
	Progress *Progress `json:"progress,omitempty"`
}

// Progress is the state of a running download.
type Progress struct {
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
	State string `json:"state"` // downloading / verifying / done / error
}

// Catalog is the built-in list of models.
var Catalog = []ModelInfo{
	{Name: "tiny-q5_1", File: "ggml-tiny-q5_1.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "tiny (q5_1)", SizeMB: 32},
	{Name: "base-q5_1", File: "ggml-base-q5_1.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "base (q5_1)", SizeMB: 60},
	{Name: "small-q5_1", File: "ggml-small-q5_1.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "small (q5_1)", SizeMB: 190},
	{Name: "medium-q5_0", File: "ggml-medium-q5_0.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "medium (q5_0)", SizeMB: 539},
	{Name: "large-v3-q5_0", File: "ggml-large-v3-q5_0.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "large-v3 (q5_0)", SizeMB: 1080},
	{Name: "large-v3-turbo-q5_0", File: "ggml-large-v3-turbo-q5_0.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "large-v3-turbo (q5_0)", SizeMB: 574},
	{Name: "tiny", File: "ggml-tiny.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "tiny (f16)", SizeMB: 75},
	{Name: "base", File: "ggml-base.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "base (f16)", SizeMB: 142},
	{Name: "small", File: "ggml-small.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "small (f16)", SizeMB: 466},
	{Name: "medium", File: "ggml-medium.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "medium (f16)", SizeMB: 1530},
	{Name: "large-v3", File: "ggml-large-v3.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "large-v3 (f16)", SizeMB: 3090},
	{Name: "large-v3-turbo", File: "ggml-large-v3-turbo.bin", Repo: "ggerganov/whisper.cpp", Kind: "whisper", Label: "large-v3-turbo (f16)", SizeMB: 1620},
	{Name: "kotoba-whisper-v2.0-q5_0", File: "ggml-kotoba-whisper-v2.0-q5_0.bin", Repo: "kotoba-tech/kotoba-whisper-v2.0-ggml", Kind: "whisper", Label: "kotoba-whisper v2.0 日本語特化 (q5_0)", SizeMB: 513},
	{Name: "kotoba-whisper-v2.0", File: "ggml-kotoba-whisper-v2.0.bin", Repo: "kotoba-tech/kotoba-whisper-v2.0-ggml", Kind: "whisper", Label: "kotoba-whisper v2.0 日本語特化 (f16)", SizeMB: 1449},
	{Name: "silero-vad", File: "ggml-silero-v5.1.2.bin", Repo: "ggml-org/whisper-vad", Kind: "vad", Label: "Silero VAD v5.1.2", SizeMB: 1},
}

// Models manages the models/ directory and downloads.
type Models struct {
	Dir     string
	mu      sync.Mutex
	prog    map[string]*Progress
	client  *http.Client
	baseURL string // for tests
}

// NewModels creates a manager for dir.
func NewModels(dir string) *Models {
	return &Models{Dir: dir, prog: map[string]*Progress{}, client: &http.Client{Timeout: 0}, baseURL: "https://huggingface.co"}
}

// Find returns the catalog entry by name.
func Find(name string) (ModelInfo, bool) {
	for _, m := range Catalog {
		if m.Name == name {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// Path returns the local path of a model by name ("" when absent).
func (m *Models) Path(name string) string {
	info, ok := Find(name)
	var p string
	if ok {
		p = filepath.Join(m.Dir, info.File)
	} else {
		// allow arbitrary file names placed by the user
		p = filepath.Join(m.Dir, name)
		if _, err := os.Stat(p); err != nil {
			p = filepath.Join(m.Dir, "ggml-"+name+".bin")
		}
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// List returns the catalog with presence and progress.
func (m *Models) List() []ModelInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ModelInfo, 0, len(Catalog))
	for _, c := range Catalog {
		if st, err := os.Stat(filepath.Join(m.Dir, c.File)); err == nil && !st.IsDir() {
			c.Present = true
			c.SizeMB = int(st.Size() / (1024 * 1024))
		}
		if p, ok := m.prog[c.Name]; ok {
			cp := *p
			c.Progress = &cp
		}
		out = append(out, c)
	}
	return out
}

// Download starts fetching a model in the background.
func (m *Models) Download(name string) error {
	info, ok := Find(name)
	if !ok {
		return errors.New("unknown_model")
	}
	m.mu.Lock()
	if p, ok := m.prog[name]; ok && (p.State == "downloading" || p.State == "verifying") {
		m.mu.Unlock()
		return errors.New("download_in_progress")
	}
	m.prog[name] = &Progress{State: "downloading"}
	m.mu.Unlock()
	go m.run(info)
	return nil
}

var lfsRe = regexp.MustCompile(`(?m)^oid sha256:([0-9a-f]{64})\s*$`)
var lfsSize = regexp.MustCompile(`(?m)^size (\d+)\s*$`)

func (m *Models) set(name string, fn func(p *Progress)) {
	m.mu.Lock()
	if p, ok := m.prog[name]; ok {
		fn(p)
	}
	m.mu.Unlock()
}

func (m *Models) fail(name string, err error) {
	m.set(name, func(p *Progress) { p.State = "error"; p.Error = err.Error() })
}

func (m *Models) run(info ModelInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		m.fail(info.Name, err)
		return
	}
	// 1. fetch the LFS pointer to learn the expected sha256 and size
	wantHash, wantSize := "", int64(0)
	if raw, err := m.get(ctx, fmt.Sprintf("%s/%s/raw/main/%s", m.baseURL, info.Repo, info.File)); err == nil {
		if mm := lfsRe.FindSubmatch(raw); mm != nil {
			wantHash = string(mm[1])
		}
		if mm := lfsSize.FindSubmatch(raw); mm != nil {
			wantSize, _ = strconv.ParseInt(string(mm[1]), 10, 64)
		}
	}
	// 2. download to a temp file
	url := fmt.Sprintf("%s/%s/resolve/main/%s", m.baseURL, info.Repo, info.File)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := m.client.Do(req)
	if err != nil {
		m.fail(info.Name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		m.fail(info.Name, fmt.Errorf("http_%d", resp.StatusCode))
		return
	}
	total := resp.ContentLength
	if total <= 0 {
		total = wantSize
	}
	m.set(info.Name, func(p *Progress) { p.Total = total })
	dst := filepath.Join(m.Dir, info.File)
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		m.fail(info.Name, err)
		return
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
				m.fail(info.Name, werr)
				return
			}
			h.Write(buf[:n])
			done += int64(n)
			m.set(info.Name, func(p *Progress) { p.Done = done })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			m.fail(info.Name, rerr)
			return
		}
	}
	f.Close()
	m.set(info.Name, func(p *Progress) { p.State = "verifying" })
	got := hex.EncodeToString(h.Sum(nil))
	if wantHash != "" && got != wantHash {
		os.Remove(tmp)
		m.fail(info.Name, fmt.Errorf("checksum_mismatch"))
		return
	}
	if wantSize > 0 && done != wantSize {
		os.Remove(tmp)
		m.fail(info.Name, fmt.Errorf("size_mismatch"))
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		m.fail(info.Name, err)
		return
	}
	m.set(info.Name, func(p *Progress) { p.State = "done" })
	// forget the progress entry after a while so the UI shows "present"
	time.AfterFunc(10*time.Second, func() {
		m.mu.Lock()
		delete(m.prog, info.Name)
		m.mu.Unlock()
	})
}

func (m *Models) get(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http_%d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4096))
}
