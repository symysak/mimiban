package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcriber turns an audio file into segments. It is an interface so tests
// can substitute a fake.
type Transcriber interface {
	Transcribe(ctx context.Context, audioPath string, language, prompt string) ([]Segment, error)
}

// HTTPClient talks to whisper-server's /inference endpoint.
type HTTPClient struct {
	BaseURL func() string
	Client  *http.Client
}

// NewHTTPClient builds a client bound to a Server.
func NewHTTPClient(base func() string) *HTTPClient {
	return &HTTPClient{BaseURL: base, Client: &http.Client{Timeout: 10 * time.Minute}}
}

type verboseResp struct {
	Text     string `json:"text"`
	Segments []struct {
		Text         string   `json:"text"`
		NoSpeechProb *float64 `json:"no_speech_prob"`
	} `json:"segments"`
	Transcription []struct {
		Text string `json:"text"`
	} `json:"transcription"`
	Error string `json:"error"`
}

// Transcribe implements Transcriber.
func (c *HTTPClient) Transcribe(ctx context.Context, audioPath, language, prompt string) ([]Segment, error) {
	base := c.BaseURL()
	if base == "" {
		return nil, errors.New("whisper_server_not_ready")
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, err
	}
	fields := map[string]string{
		"response_format": "verbose_json",
		"temperature":     "0",
		"no_timestamps":   "false",
	}
	if language != "" {
		fields["language"] = language
	}
	if prompt != "" {
		fields["prompt"] = prompt
	}
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/inference", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("whisper_server_http_%d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return ParseResponse(raw)
}

// ParseResponse converts whisper-server JSON (json or verbose_json) into segments.
func ParseResponse(raw []byte) ([]Segment, error) {
	var v verboseResp
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("whisper_server_bad_json: %s", strings.TrimSpace(string(raw)))
	}
	if v.Error != "" {
		return nil, errors.New(v.Error)
	}
	var segs []Segment
	for _, s := range v.Segments {
		seg := Segment{Text: s.Text}
		if s.NoSpeechProb != nil {
			seg.NoSpeechProb = *s.NoSpeechProb
			seg.HasProb = true
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		for _, s := range v.Transcription {
			segs = append(segs, Segment{Text: s.Text})
		}
	}
	if len(segs) == 0 && strings.TrimSpace(v.Text) != "" {
		segs = append(segs, Segment{Text: v.Text})
	}
	return segs, nil
}
