// Package api exposes the JSON HTTP API and serves the embedded UI.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/engine"
	"github.com/symysak/mimiban/internal/notify"
	"github.com/symysak/mimiban/internal/store"
)

// Server holds the dependencies of the handlers.
type Server struct {
	Cfg      *config.Store
	DB       *store.DB
	Engine   *engine.Engine
	Models   *asr.Models
	Notifier *notify.Sender
	Web      fs.FS
	Version  string
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", s.devices)
	mux.HandleFunc("POST /api/devices/refresh", s.devicesRefresh)
	mux.HandleFunc("GET /api/network", s.network)
	mux.HandleFunc("GET /api/receivers", s.receivers)
	mux.HandleFunc("POST /api/receivers", s.receiverCreate)
	mux.HandleFunc("PUT /api/receivers/{id}", s.receiverUpdate)
	mux.HandleFunc("DELETE /api/receivers/{id}", s.receiverDelete)
	mux.HandleFunc("POST /api/receivers/start_all", s.startAll)
	mux.HandleFunc("POST /api/receivers/stop_all", s.stopAll)
	mux.HandleFunc("POST /api/receivers/{id}/start", s.receiverStart)
	mux.HandleFunc("POST /api/receivers/{id}/stop", s.receiverStop)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/config", s.configGet)
	mux.HandleFunc("PUT /api/config", s.configPut)
	mux.HandleFunc("GET /api/calls", s.calls)
	mux.HandleFunc("GET /api/calls/{id}", s.callGet)
	mux.HandleFunc("GET /api/calls/{id}/audio", s.callAudio)
	mux.HandleFunc("PUT /api/calls/{id}/transcript", s.callTranscript)
	mux.HandleFunc("POST /api/calls/{id}/transcribe", s.callTranscribe)
	mux.HandleFunc("DELETE /api/calls/{id}", s.callDelete)
	mux.HandleFunc("POST /api/transcribe/pending", s.transcribePending)
	mux.HandleFunc("POST /api/transcribe/all", s.transcribeAll)
	mux.HandleFunc("POST /api/dictionary/apply", s.dictionaryApply)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/disk", s.disk)
	mux.HandleFunc("GET /api/webhooks", s.webhooks)
	mux.HandleFunc("POST /api/webhooks", s.webhookCreate)
	mux.HandleFunc("PUT /api/webhooks/{id}", s.webhookUpdate)
	mux.HandleFunc("DELETE /api/webhooks/{id}", s.webhookDelete)
	mux.HandleFunc("POST /api/webhooks/{id}/test", s.webhookTest)
	mux.HandleFunc("GET /api/notify/log", s.notifyLog)
	mux.HandleFunc("GET /api/models", s.models)
	mux.HandleFunc("POST /api/models/{name}/download", s.modelDownload)
	mux.HandleFunc("POST /api/asr/restart", s.asrRestart)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"version": s.Version})
	})
	if s.Web != nil {
		mux.Handle("/", noCacheHTML(http.FileServer(http.FS(s.Web))))
	}
	return mux
}

func noCacheHTML(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, ".json") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, apiError{Error: err.Error()})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(v)
}

func newID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ---- receivers ----

type receiverView struct {
	config.Receiver
	Status engine.ReceiverStatus `json:"status"`
}

func (s *Server) receivers(w http.ResponseWriter, _ *http.Request) {
	cfg := s.Cfg.Get()
	st := s.Engine.Status()
	out := make([]receiverView, 0, len(cfg.Receivers))
	for _, r := range cfg.Receivers {
		out = append(out, receiverView{Receiver: r, Status: st.Receivers[r.ID]})
	}
	writeJSON(w, 200, out)
}

func (s *Server) receiverCreate(w http.ResponseWriter, r *http.Request) {
	in := config.DefaultReceiver()
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	in.ID = newID("rx_")
	cfg := s.Cfg.Get()
	if err := engine.ValidateReceiver(cfg, in, "", cfg.Port); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.Cfg.Update(func(c *config.Config) error {
		c.Receivers = append(c.Receivers, in)
		return nil
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	if in.Enabled {
		_ = s.Engine.StartReceiver(in.ID)
	}
	writeJSON(w, 201, in)
}

func (s *Server) receiverUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.Cfg.Get()
	cur := cfg.FindReceiver(id)
	if cur == nil {
		writeErr(w, 404, errors.New("receiver_not_found"))
		return
	}
	in := *cur
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	in.ID = id
	if err := engine.ValidateReceiver(cfg, in, id, cfg.Port); err != nil {
		writeErr(w, 400, err)
		return
	}
	sourceChanged := in.SourceType != cur.SourceType || in.DeviceName != cur.DeviceName || in.ListenPort != cur.ListenPort ||
		in.InputURL != cur.InputURL || in.ChannelSelect != cur.ChannelSelect || in.StreamPassphrase != cur.StreamPassphrase ||
		in.InputFormat != cur.InputFormat || in.InputRate != cur.InputRate || in.InputChannels != cur.InputChannels
	if err := s.Cfg.Update(func(c *config.Config) error {
		if p := c.FindReceiver(id); p != nil {
			*p = in
		}
		return nil
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	if s.Engine.Running(id) && sourceChanged {
		_ = s.Engine.RestartReceiver(id)
	} else {
		s.Engine.ApplyConfig()
	}
	writeJSON(w, 200, in)
}

func (s *Server) receiverDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.Engine.StopReceiver(id)
	err := s.Cfg.Update(func(c *config.Config) error {
		out := c.Receivers[:0]
		for _, x := range c.Receivers {
			if x.ID != id {
				out = append(out, x)
			}
		}
		c.Receivers = out
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) receiverStart(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.StartReceiver(r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) receiverStop(w http.ResponseWriter, r *http.Request) {
	_ = s.Engine.StopReceiver(r.PathValue("id"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) startAll(w http.ResponseWriter, _ *http.Request) {
	s.Engine.StartAll()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) stopAll(w http.ResponseWriter, _ *http.Request) {
	s.Engine.StopAll()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- devices / status / config ----

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Engine.Devices())
}

func (s *Server) devicesRefresh(w http.ResponseWriter, _ *http.Request) {
	s.Engine.RefreshDevices()
	writeJSON(w, 200, s.Engine.Devices())
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Engine.Status())
}

// configView hides receivers/webhooks (they have their own endpoints).
type configView struct {
	config.Config
	Receivers *struct{} `json:"receivers,omitempty"`
}

func (s *Server) configGet(w http.ResponseWriter, _ *http.Request) {
	cfg := s.Cfg.Get()
	cfg.Receivers = nil
	cfg.Notify.Webhooks = nil
	writeJSON(w, 200, cfg)
}

func (s *Server) configPut(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	delete(patch, "receivers")
	err := s.Cfg.Update(func(c *config.Config) error {
		// keep webhooks managed by their own endpoints
		webhooks := c.Notify.Webhooks
		b, _ := json.Marshal(patch)
		if err := json.Unmarshal(b, c); err != nil {
			return errors.New("bad_config")
		}
		c.Notify.Webhooks = webhooks
		if c.Port <= 0 || c.Port > 65535 {
			return errors.New("invalid_port")
		}
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.Engine.ApplyConfig()
	s.configGet(w, r)
}
