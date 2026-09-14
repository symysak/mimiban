// Package notify sends Discord / generic JSON webhooks for calls. It runs on
// its own queue so that failures never affect recording or ASR.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/store"
)

// Templates are the language specific strings for the notification body.
type Templates struct {
	Transcribing string `json:"transcribing"`
	FieldTime    string `json:"field_time"`
	FieldLength  string `json:"field_length"`
	FieldPeak    string `json:"field_peak"`
	Truncated    string `json:"truncated"`
	MoreCalls    string `json:"more_calls"`
	OpenInUI     string `json:"open_in_ui"`
	TestTitle    string `json:"test_title"`
	TestBody     string `json:"test_body"`
	Seconds      string `json:"seconds"`
}

// Job is one notification to send.
type Job struct {
	Event    string // call / transcript / test
	Call     *store.Call
	Receiver config.Receiver
	Webhook  config.Webhook
	// Edit is set when a previous "call" message should be edited (timing=both).
	Edit    bool
	Attempt int
}

// Sender delivers jobs. Logger persists results.
type Sender struct {
	mu       sync.Mutex
	cfg      func() config.Config
	tmpl     func(lang string) Templates
	baseURL  func() string
	db       *store.DB
	client   *http.Client
	queue    chan Job
	messages map[string]string // key callID:webhookID → discord message id
	// per-webhook rate limiter
	sent    map[string][]time.Time
	merged  map[string]int
	Logf    func(string, ...any)
	ctx     context.Context
	cancel  context.CancelFunc
	sendNow func(job Job) (int, string, error) // test hook
}

// New creates a sender. baseURL supplies the UI link prefix.
func New(cfg func() config.Config, tmpl func(string) Templates, baseURL func() string, db *store.DB) *Sender {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sender{
		cfg: cfg, tmpl: tmpl, baseURL: baseURL, db: db,
		client:   &http.Client{Timeout: 30 * time.Second},
		queue:    make(chan Job, 1024),
		messages: map[string]string{},
		sent:     map[string][]time.Time{},
		merged:   map[string]int{},
		Logf:     func(string, ...any) {},
		ctx:      ctx, cancel: cancel,
	}
	return s
}

// Start launches the single worker.
func (s *Sender) Start() { go s.worker() }

// Stop ends the worker.
func (s *Sender) Stop() { s.cancel() }

// OnCall is called right after a call is saved (before ASR).
func (s *Sender) OnCall(c *store.Call, rx config.Receiver) {
	s.dispatch("call", c, rx)
}

// OnTranscript is called when ASR finished with status done.
func (s *Sender) OnTranscript(c *store.Call, rx config.Receiver) {
	s.dispatch("transcript", c, rx)
}

func (s *Sender) dispatch(event string, c *store.Call, rx config.Receiver) {
	cfg := s.cfg()
	if !cfg.Notify.Enabled {
		return
	}
	for _, w := range cfg.Notify.Webhooks {
		ok, edit := ShouldNotify(w, cfg.Notify, event, c)
		if !ok {
			continue
		}
		s.enqueue(Job{Event: event, Call: c, Receiver: rx, Webhook: w, Edit: edit})
	}
}

// Test enqueues a dummy call for webhook w.
func (s *Sender) Test(w config.Webhook) {
	now := time.Now()
	peak := -20.0
	c := &store.Call{ID: 0, ReceiverID: "test", Channel: "Mimiban", StartedAt: store.FormatTime(now), Duration: 3.2, PeakDB: &peak, ASRStatus: "done"}
	s.enqueue(Job{Event: "test", Call: c, Receiver: config.Receiver{ID: "test", Name: "Mimiban", Color: "#3b82f6"}, Webhook: w})
}

func (s *Sender) enqueue(j Job) {
	select {
	case s.queue <- j:
	default:
		s.Logf("notify queue full, dropping job for webhook %s", j.Webhook.ID)
	}
}

// ShouldNotify evaluates receiver / keyword / min-duration / timing filters.
// The second result is true when the transcript event must edit an existing
// message (timing=both).
func ShouldNotify(w config.Webhook, n config.Notify, event string, c *store.Call) (bool, bool) {
	if !w.Enabled || w.URL == "" {
		return false, false
	}
	if c.Duration < n.MinDurationSec {
		return false, false
	}
	if len(w.ReceiverIDs) > 0 && !contains(w.ReceiverIDs, c.ReceiverID) {
		return false, false
	}
	switch w.Timing {
	case "on_call":
		if event != "call" {
			return false, false
		}
		return true, false // keywords ignored: no transcript yet
	case "both":
		if event == "call" {
			return true, false
		}
		if event != "transcript" {
			return false, false
		}
		if !keywordOK(w, c) {
			return false, false
		}
		return true, true
	default: // on_transcript
		if event != "transcript" {
			return false, false
		}
		return keywordOK(w, c), false
	}
}

func keywordOK(w config.Webhook, c *store.Call) bool {
	if len(w.Keywords) == 0 {
		return true
	}
	text := ""
	if c.Transcript != nil {
		text = strings.ToLower(*c.Transcript)
	}
	hit := false
	for _, k := range w.Keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" && strings.Contains(text, k) {
			hit = true
			break
		}
	}
	if w.KeywordMode == "exclude" {
		return !hit
	}
	return hit
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// allowSend applies the per-webhook per-minute limit. When exceeded the job
// is counted as merged and false is returned.
func (s *Sender) allowSend(webhookID string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var keep []time.Time
	for _, t := range s.sent[webhookID] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	s.sent[webhookID] = keep
	if limit > 0 && len(keep) >= limit {
		s.merged[webhookID]++
		return false
	}
	s.sent[webhookID] = append(keep, now)
	return true
}

func (s *Sender) takeMerged(webhookID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.merged[webhookID]
	s.merged[webhookID] = 0
	return n
}

func (s *Sender) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			s.process(j)
		}
	}
}

func (s *Sender) process(j Job) {
	cfg := s.cfg()
	var callID *int64
	if j.Call != nil && j.Call.ID > 0 {
		id := j.Call.ID
		callID = &id
	}
	if j.Event != "test" && !s.allowSend(j.Webhook.ID, cfg.Notify.RateLimitPerMin) {
		s.log(store.NotifyLog{CallID: callID, WebhookID: j.Webhook.ID, Status: "merged"})
		return
	}
	more := s.takeMerged(j.Webhook.ID)
	status, body, err := s.send(j, more)
	if err == nil && status < 300 {
		s.log(store.NotifyLog{CallID: callID, WebhookID: j.Webhook.ID, Status: "sent", HTTPStatus: &status})
		return
	}
	// 429: respect Retry-After; 5xx/network: exponential backoff up to 5 tries
	retry := false
	var wait time.Duration
	if status == 429 {
		retry = true
		wait = 2 * time.Second
		if ra := retryAfter(body); ra > 0 {
			wait = ra
		}
	} else if status >= 500 || status == 0 {
		retry = j.Attempt < 5
		wait = time.Duration(1<<uint(j.Attempt)) * time.Second
	}
	if retry && j.Attempt < 8 {
		j.Attempt++
		go func() {
			select {
			case <-time.After(wait):
				s.enqueue(j)
			case <-s.ctx.Done():
			}
		}()
		return
	}
	msg := body
	if err != nil {
		msg = err.Error()
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	hs := &status
	if status == 0 {
		hs = nil
	}
	s.log(store.NotifyLog{CallID: callID, WebhookID: j.Webhook.ID, Status: "failed", HTTPStatus: hs, Error: &msg})
}

func retryAfter(body string) time.Duration {
	// Discord returns {"retry_after": 1.23} in the body
	var v struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal([]byte(body), &v) == nil && v.RetryAfter > 0 {
		return time.Duration(v.RetryAfter*1000) * time.Millisecond
	}
	return 0
}

func (s *Sender) log(l store.NotifyLog) {
	l.SentAt = store.FormatTime(time.Now())
	if s.db != nil {
		if err := s.db.InsertNotifyLog(l); err != nil {
			s.Logf("notify_log insert: %v", err)
		}
	}
	if l.Status == "failed" && l.Error != nil {
		s.Logf("notify failed webhook=%s: %s", l.WebhookID, *l.Error)
	}
}

// send performs one HTTP delivery and returns the status, body and error.
func (s *Sender) send(j Job, more int) (int, string, error) {
	if s.sendNow != nil {
		return s.sendNow(j)
	}
	cfg := s.cfg()
	tm := s.tmpl(cfg.NotifyLanguage())
	link := s.baseURL()
	if j.Call != nil && j.Call.ID > 0 {
		link = strings.TrimRight(link, "/") + "/#/calls/" + strconv.FormatInt(j.Call.ID, 10)
	}
	if j.Webhook.Type == "generic_json" {
		payload := GenericPayload(j, link)
		b, _ := json.Marshal(payload)
		return s.post(j.Webhook.URL, "application/json", bytes.NewReader(b), nil)
	}
	key := fmt.Sprintf("%d:%s", callIDOf(j.Call), j.Webhook.ID)
	payload := DiscordPayload(j, tm, link, more)
	if j.Edit {
		s.mu.Lock()
		mid := s.messages[key]
		delete(s.messages, key)
		s.mu.Unlock()
		if mid != "" {
			b, _ := json.Marshal(payload)
			st, body, err := s.do(http.MethodPatch, j.Webhook.URL+"/messages/"+mid, "application/json", bytes.NewReader(b), nil)
			if err == nil && st < 300 {
				return st, body, nil
			}
			// fall back to a new message
		}
	}
	url := j.Webhook.URL
	wantID := j.Webhook.Timing == "both" && j.Event == "call"
	if wantID {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "wait=true"
	}
	var st int
	var body string
	var err error
	if j.Webhook.AttachAudio && j.Call != nil && j.Call.Path != "" && fileSizeOK(j.Call.Path) {
		st, body, err = s.postMultipart(url, payload, j.Call.Path)
	} else {
		b, _ := json.Marshal(payload)
		st, body, err = s.post(url, "application/json", bytes.NewReader(b), nil)
	}
	if wantID && err == nil && st < 300 {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(body), &v) == nil && v.ID != "" {
			s.mu.Lock()
			s.messages[key] = v.ID
			s.mu.Unlock()
		}
	}
	return st, body, err
}

func callIDOf(c *store.Call) int64 {
	if c == nil {
		return 0
	}
	return c.ID
}

const discordMaxFile = 8 << 20

func fileSizeOK(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() <= discordMaxFile
}

func (s *Sender) post(url, ct string, body io.Reader, hdr map[string]string) (int, string, error) {
	return s.do(http.MethodPost, url, ct, body, hdr)
}

func (s *Sender) do(method, url, ct string, body io.Reader, hdr map[string]string) (int, string, error) {
	req, err := http.NewRequestWithContext(s.ctx, method, url, body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("User-Agent", "Mimiban/1.0")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, string(b), nil
}

func (s *Sender) postMultipart(url string, payload map[string]any, audioPath string) (int, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	pj, _ := json.Marshal(payload)
	_ = w.WriteField("payload_json", string(pj))
	fw, err := w.CreateFormFile("files[0]", filepath.Base(audioPath))
	if err != nil {
		return 0, "", err
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return 0, "", err
	}
	_, err = io.Copy(fw, f)
	f.Close()
	if err != nil {
		return 0, "", err
	}
	w.Close()
	return s.post(url, w.FormDataContentType(), &buf, nil)
}
