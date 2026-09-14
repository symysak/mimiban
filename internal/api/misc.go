package api

import (
	"errors"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/symysak/mimiban/internal/config"
)

// ---- webhooks ----

func (s *Server) webhooks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Cfg.Get().Notify.Webhooks)
}

func validateWebhook(wh config.Webhook) error {
	if wh.Name == "" {
		return errors.New("name_required")
	}
	if !strings.HasPrefix(wh.URL, "http://") && !strings.HasPrefix(wh.URL, "https://") {
		return errors.New("url_invalid")
	}
	switch wh.Type {
	case "discord", "generic_json":
	default:
		return errors.New("invalid_webhook_type")
	}
	switch wh.Timing {
	case "on_call", "on_transcript", "both":
	default:
		return errors.New("invalid_timing")
	}
	switch wh.KeywordMode {
	case "any", "exclude":
	default:
		return errors.New("invalid_keyword_mode")
	}
	return nil
}

func (s *Server) webhookCreate(w http.ResponseWriter, r *http.Request) {
	in := config.Webhook{Type: "discord", Enabled: true, Timing: "on_transcript", KeywordMode: "any", ReceiverIDs: []string{}, Keywords: []string{}}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	in.ID = newID("wh_")
	if err := validateWebhook(in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.Cfg.Update(func(c *config.Config) error {
		c.Notify.Webhooks = append(c.Notify.Webhooks, in)
		return nil
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, in)
}

func (s *Server) webhookUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.Cfg.Get()
	cur := cfg.FindWebhook(id)
	if cur == nil {
		writeErr(w, 404, errors.New("webhook_not_found"))
		return
	}
	in := *cur
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	in.ID = id
	if err := validateWebhook(in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.Cfg.Update(func(c *config.Config) error {
		if p := c.FindWebhook(id); p != nil {
			*p = in
		}
		return nil
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, in)
}

func (s *Server) webhookDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Cfg.Update(func(c *config.Config) error {
		out := c.Notify.Webhooks[:0]
		for _, x := range c.Notify.Webhooks {
			if x.ID != id {
				out = append(out, x)
			}
		}
		c.Notify.Webhooks = out
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) webhookTest(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Get()
	wh := cfg.FindWebhook(r.PathValue("id"))
	if wh == nil {
		writeErr(w, 404, errors.New("webhook_not_found"))
		return
	}
	if s.Notifier == nil {
		writeErr(w, 500, errors.New("notifier_unavailable"))
		return
	}
	t := *wh
	t.Enabled = true
	s.Notifier.Test(t)
	writeJSON(w, 200, map[string]bool{"queued": true})
}

func (s *Server) notifyLog(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.DB.RecentNotifyLog(50)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// ---- models / asr ----

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	if s.Models == nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, s.Models.List())
}

func (s *Server) modelDownload(w http.ResponseWriter, r *http.Request) {
	if s.Models == nil {
		writeErr(w, 500, errors.New("models_unavailable"))
		return
	}
	if err := s.Models.Download(r.PathValue("name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"started": true})
}

func (s *Server) asrRestart(w http.ResponseWriter, _ *http.Request) {
	if err := s.Engine.RestartASR(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- network ----

// NetworkInfo is the /api/network payload.
type NetworkInfo struct {
	TailscaleIP string   `json:"tailscale_ip"`
	LANIPs      []string `json:"lan_ips"`
	HTTPPort    int      `json:"http_port"`
	UsedPorts   []int    `json:"used_ports"`
}

func (s *Server) network(w http.ResponseWriter, _ *http.Request) {
	cfg := s.Cfg.Get()
	info := NetworkInfo{HTTPPort: cfg.Port, LANIPs: []string{}, UsedPorts: []int{cfg.Port}}
	for _, r := range cfg.Receivers {
		if r.SourceType == "stream" && r.ListenPort > 0 {
			info.UsedPorts = append(info.UsedPorts, r.ListenPort)
		}
	}
	info.TailscaleIP = TailscaleIP()
	info.LANIPs = lanIPs()
	writeJSON(w, 200, info)
}

// TailscaleIP runs `tailscale ip -4` when available.
func TailscaleIP() string {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		for _, p := range []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", `C:\Program Files\Tailscale\tailscale.exe`} {
			if _, err := exec.LookPath(p); err == nil {
				path = p
				break
			}
		}
		if path == "" {
			return ""
		}
	}
	cmd := exec.Command(path, "ip", "-4")
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		return ""
	}
	ip := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if p := net.ParseIP(ip); p == nil || p.To4() == nil {
		return ""
	}
	return ip
}

func lanIPs() []string {
	var out []string
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4()
			if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
				continue // tailscale CGNAT range is reported separately
			}
			out = append(out, ip.String())
		}
	}
	return out
}
