package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/capture"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/engine"
	"github.com/symysak/mimiban/internal/store"
)

type nopCapture struct{ on func(string, string) }

func (n *nopCapture) Start(ctx context.Context) (<-chan capture.Block, error) {
	ch := make(chan capture.Block)
	n.on(capture.StateListening, "")
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func (n *nopCapture) Stop() {}

type enum struct{}

func (enum) Enumerate() ([]capture.Device, error) {
	return []capture.Device{{Index: 0, Name: "Dev A", Channels: 2}}, nil
}

type fakeASR struct{}

func (fakeASR) Transcribe(context.Context, string, string, string) ([]asr.Segment, error) {
	return []asr.Segment{{Text: "ok"}}, nil
}

func newServer(t *testing.T) (*Server, *httptest.Server) {
	dir := t.TempDir()
	cs, _ := config.Load(filepath.Join(dir, "config.json"))
	db, _ := store.Open(filepath.Join(dir, "t.sqlite3"))
	eng := engine.New(engine.Options{Config: cs, DB: db, BaseDir: dir, Enumerator: enum{},
		Factory:     func(_ capture.Source, on func(string, string)) capture.Capture { return &nopCapture{on: on} },
		Transcriber: fakeASR{}, Encoder: engine.WAVEncoder{}, Logf: t.Logf})
	eng.Start()
	s := &Server{Cfg: cs, DB: db, Engine: eng, Models: asr.NewModels(filepath.Join(dir, "models"))}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); eng.Stop(); db.Close() })
	return s, ts
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any) (int, map[string]any, []byte) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(buf.Bytes(), &m)
	return resp.StatusCode, m, buf.Bytes()
}

func TestSmoke(t *testing.T) {
	_, ts := newServer(t)
	code, _, raw := do(t, ts, "GET", "/api/devices", nil)
	if code != 200 || !bytes.Contains(raw, []byte("Dev A")) {
		t.Fatalf("devices %d %s", code, raw)
	}
	// create receivers: mono then left on same device must conflict
	code, r1, _ := do(t, ts, "POST", "/api/receivers", map[string]any{"name": "A", "source_type": "device", "device_name": "Dev A", "channel_select": "left", "enabled": false})
	if code != 201 {
		t.Fatalf("create: %d %v", code, r1)
	}
	code, e, _ := do(t, ts, "POST", "/api/receivers", map[string]any{"name": "B", "source_type": "device", "device_name": "Dev A", "channel_select": "mono"})
	if code != 400 || e["error"] != "device_channel_conflict" {
		t.Fatalf("conflict expected: %d %v", code, e)
	}
	code, e, _ = do(t, ts, "POST", "/api/receivers", map[string]any{"name": "B", "source_type": "device", "device_name": "Dev A", "channel_select": "left"})
	if code != 400 || e["error"] != "device_channel_duplicate" {
		t.Fatalf("duplicate expected: %d %v", code, e)
	}
	code, _, _ = do(t, ts, "POST", "/api/receivers", map[string]any{"name": "B", "source_type": "device", "device_name": "Dev A", "channel_select": "right", "enabled": false})
	if code != 201 {
		t.Fatalf("right should pass: %d", code)
	}
	id := r1["id"].(string)
	// start / status
	code, _, _ = do(t, ts, "POST", "/api/receivers/"+id+"/start", nil)
	if code != 200 {
		t.Fatalf("start %d", code)
	}
	start := time.Now()
	code, st, _ := do(t, ts, "GET", "/api/status", nil)
	if code != 200 || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("status %d took %s", code, time.Since(start))
	}
	rx := st["receivers"].(map[string]any)[id].(map[string]any)
	if rx["state"] != "listening" {
		t.Fatalf("state %v", rx["state"])
	}
	// update threshold live
	code, _, _ = do(t, ts, "PUT", "/api/receivers/"+id, map[string]any{"vox_threshold_db": -20})
	if code != 200 {
		t.Fatalf("update %d", code)
	}
	// config
	code, cfg, _ := do(t, ts, "PUT", "/api/config", map[string]any{"retention_days": 3, "dictionary": []map[string]string{{"from": "a", "to": "b"}}})
	if code != 200 || cfg["retention_days"].(float64) != 3 {
		t.Fatalf("config %d %v", code, cfg)
	}
	// calls empty
	code, calls, _ := do(t, ts, "GET", "/api/calls?q=x&receiver_id="+id, nil)
	if code != 200 || calls["total"].(float64) != 0 {
		t.Fatalf("calls %d %v", code, calls)
	}
	code, _, _ = do(t, ts, "GET", "/api/stats?days=7", nil)
	if code != 200 {
		t.Fatal("stats")
	}
	// webhooks
	code, wh, _ := do(t, ts, "POST", "/api/webhooks", map[string]any{"name": "d", "url": "https://discord.com/api/webhooks/x", "type": "discord"})
	if code != 201 {
		t.Fatalf("webhook create %d %v", code, wh)
	}
	code, _, _ = do(t, ts, "DELETE", "/api/webhooks/"+wh["id"].(string), nil)
	if code != 200 {
		t.Fatal("webhook delete")
	}
	code, _, raw = do(t, ts, "GET", "/api/models", nil)
	if code != 200 || !bytes.Contains(raw, []byte("large-v3-turbo")) {
		t.Fatalf("models %d", code)
	}
	code, _, _ = do(t, ts, "GET", "/api/network", nil)
	if code != 200 {
		t.Fatal("network")
	}
	code, _, _ = do(t, ts, "DELETE", "/api/receivers/"+id, nil)
	if code != 200 {
		t.Fatal("delete rx")
	}
	code, _, _ = do(t, ts, "GET", "/api/calls/999/audio", nil)
	if code != 404 {
		t.Fatalf("missing audio should be 404, got %d", code)
	}
}
