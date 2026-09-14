package asr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server states.
const (
	SrvStopped  = "stopped"
	SrvStarting = "starting"
	SrvReady    = "ready"
	SrvError    = "error"
)

// ServerOptions configure a whisper-server launch.
type ServerOptions struct {
	Binary     string
	ModelPath  string
	VADModel   string // "" = no VAD
	Language   string
	Threads    int
	IdleUnload time.Duration // 0 = keep resident
}

// ServerStatus is a snapshot for /api/status.
type ServerStatus struct {
	State     string `json:"state"`
	Model     string `json:"model"`
	GPU       bool   `json:"gpu"`
	GPUInfo   string `json:"gpu_info"`
	Port      int    `json:"port"`
	LastError string `json:"last_error"`
}

// Server supervises the single whisper-server child process.
type Server struct {
	mu       sync.Mutex
	opts     ServerOptions
	cmd      *exec.Cmd
	port     int
	state    string
	lastErr  string
	gpu      bool
	gpuInfo  string
	lastUsed time.Time
	stopCh   chan struct{}
	waitDone chan struct{}
	exitErr  error
	// Logf receives one-line log messages.
	Logf func(format string, a ...any)
}

// NewServer creates an idle supervisor.
func NewServer(opts ServerOptions) *Server {
	s := &Server{opts: opts, state: SrvStopped, Logf: log.Printf}
	go s.idleLoop()
	return s
}

// SetOptions replaces the launch options; a running server is restarted
// when the model, VAD model, language or threads change.
func (s *Server) SetOptions(o ServerOptions) {
	s.mu.Lock()
	changed := s.opts != o
	s.opts = o
	running := s.cmd != nil
	s.mu.Unlock()
	if changed && running {
		s.Stop()
	}
}

// Status returns a snapshot.
func (s *Server) Status() ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ServerStatus{State: s.state, Model: s.opts.ModelPath, GPU: s.gpu, GPUInfo: s.gpuInfo, Port: s.port, LastError: s.lastErr}
}

// BaseURL returns http://127.0.0.1:<port> ("" when stopped).
func (s *Server) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == 0 || s.state != SrvReady {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(s.port)
}

// Touch records activity (defers idle unload).
func (s *Server) Touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

// Ensure starts the server when needed and blocks until it is ready.
func (s *Server) Ensure(ctx context.Context) error {
	s.mu.Lock()
	if s.state == SrvReady && s.cmd != nil {
		s.lastUsed = time.Now()
		s.mu.Unlock()
		return nil
	}
	if s.cmd != nil {
		// stale/errored process: tear down before restarting
		s.mu.Unlock()
		s.Stop()
		s.mu.Lock()
	}
	opts := s.opts
	if opts.Binary == "" {
		s.state = SrvError
		s.lastErr = "whisper_server_not_found"
		s.mu.Unlock()
		return errors.New("whisper_server_not_found")
	}
	if opts.ModelPath == "" {
		s.state = SrvError
		s.lastErr = "model_not_found"
		s.mu.Unlock()
		return errors.New("model_not_found")
	}
	port, err := freePort()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	args := []string{"-m", opts.ModelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if opts.Language != "" {
		args = append(args, "-l", opts.Language)
	}
	if opts.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(opts.Threads))
	}
	if opts.VADModel != "" {
		args = append(args, "--vad", "--vad-model", opts.VADModel)
	}
	cmd := exec.Command(opts.Binary, args...)
	stderr, _ := cmd.StderrPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		s.state = SrvError
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}
	s.cmd = cmd
	s.port = port
	s.state = SrvStarting
	s.lastErr = ""
	s.gpu = false
	s.gpuInfo = ""
	s.stopCh = make(chan struct{})
	s.waitDone = make(chan struct{})
	s.lastUsed = time.Now()
	s.mu.Unlock()
	s.Logf("whisper-server starting: %s %s", opts.Binary, strings.Join(args, " "))

	go s.scanLog(stderr)
	go s.scanLog(stdout)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if s.cmd == cmd {
			s.cmd = nil
			s.exitErr = err
			if s.state != SrvStopped {
				s.state = SrvError
				if err != nil {
					s.lastErr = err.Error()
				} else {
					s.lastErr = "whisper_server_exited"
				}
			}
			s.port = 0
		}
		s.mu.Unlock()
		close(s.waitDone)
	}()

	// wait for readiness
	deadline := time.Now().Add(180 * time.Second)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			s.Stop()
			return ctx.Err()
		}
		s.mu.Lock()
		alive := s.cmd == cmd
		s.mu.Unlock()
		if !alive {
			st := s.Status()
			return fmt.Errorf("whisper_server_exited: %s", st.LastError)
		}
		if healthy(base) {
			s.mu.Lock()
			s.state = SrvReady
			s.lastUsed = time.Now()
			s.mu.Unlock()
			s.Logf("whisper-server ready on %s", base)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	s.Stop()
	return errors.New("whisper_server_start_timeout")
}

func healthy(base string) bool {
	c := &http.Client{Timeout: 1 * time.Second}
	resp, err := c.Get(base + "/health")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return true
		}
	}
	// older servers have no /health; any HTTP answer on /inference means alive
	resp, err = c.Get(base + "/inference")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func (s *Server) scanLog(r interface{ Read([]byte) (int, error) }) {
	if r == nil {
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		l := strings.ToLower(line)
		gpu := ""
		switch {
		case strings.Contains(l, "ggml_cuda_init") || strings.Contains(l, "found ") && strings.Contains(l, "cuda device"):
			gpu = "CUDA"
		case strings.Contains(l, "ggml_metal_init") || strings.Contains(l, "ggml_metal_device_init"):
			gpu = "Metal"
		case strings.Contains(l, "ggml_vulkan") || strings.Contains(l, "ggml_vk_"):
			gpu = "Vulkan"
		case strings.Contains(l, "hip") && strings.Contains(l, "ggml_cuda"):
			gpu = "ROCm"
		}
		if gpu != "" {
			s.mu.Lock()
			s.gpu = true
			if s.gpuInfo == "" {
				s.gpuInfo = gpu
			}
			s.mu.Unlock()
		}
		if strings.Contains(l, "error") || strings.Contains(l, "failed") {
			s.mu.Lock()
			s.lastErr = strings.TrimSpace(line)
			s.mu.Unlock()
		}
	}
}

// Stop terminates the process (no-op when stopped).
func (s *Server) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	done := s.waitDone
	s.state = SrvStopped
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	s.mu.Lock()
	s.cmd = nil
	s.port = 0
	s.state = SrvStopped
	s.mu.Unlock()
	s.Logf("whisper-server stopped")
}

// Restart stops and starts the server.
func (s *Server) Restart(ctx context.Context) error {
	s.Stop()
	return s.Ensure(ctx)
}

func (s *Server) idleLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		idle := s.opts.IdleUnload
		expired := s.cmd != nil && idle > 0 && time.Since(s.lastUsed) > idle
		s.mu.Unlock()
		if expired {
			s.Logf("whisper-server idle for %s, unloading", idle)
			s.Stop()
		}
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
