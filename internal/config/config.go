// Package config holds the persistent configuration (config.json) of Mimiban.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Receiver is one registered audio source with its VOX settings.
type Receiver struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	SourceType       string  `json:"source_type"` // device / stream / url
	DeviceIndex      int     `json:"device_index"`
	DeviceName       string  `json:"device_name"`
	ListenPort       int     `json:"listen_port"`
	StreamPassphrase string  `json:"stream_passphrase"`
	InputURL         string  `json:"input_url"`
	InputFormat      string  `json:"input_format"`
	InputRate        int     `json:"input_rate"`
	InputChannels    int     `json:"input_channels"`
	ChannelSelect    string  `json:"channel_select"` // mono / left / right
	Enabled          bool    `json:"enabled"`
	VoxThresholdDB   float64 `json:"vox_threshold_db"`
	SilenceSec       float64 `json:"silence_sec"`
	PrebufferSec     float64 `json:"prebuffer_sec"`
	MinCallSec       float64 `json:"min_call_sec"`
	MaxCallSec       float64 `json:"max_call_sec"`
	ASREnabled       bool    `json:"asr_enabled"`
	ASRPriority      int     `json:"asr_priority"`
	Color            string  `json:"color"`
}

// Webhook is one notification target.
type Webhook struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Type        string   `json:"type"` // discord / generic_json
	Enabled     bool     `json:"enabled"`
	ReceiverIDs []string `json:"receiver_ids"`
	Timing      string   `json:"timing"` // on_call / on_transcript / both
	Keywords    []string `json:"keywords"`
	KeywordMode string   `json:"keyword_mode"` // any / exclude
	AttachAudio bool     `json:"attach_audio"`
	Mention     string   `json:"mention"`
}

// Notify groups the notification settings.
type Notify struct {
	Enabled         bool      `json:"enabled"`
	Webhooks        []Webhook `json:"webhooks"`
	RateLimitPerMin int       `json:"rate_limit_per_min"`
	MinDurationSec  float64   `json:"min_duration_sec"`
	BaseURL         string    `json:"base_url"`
	Language        string    `json:"language"` // "" = follow ui_language
}

// DictEntry maps a misrecognised string to the correct spelling.
type DictEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Config is the whole config.json.
type Config struct {
	Host                string              `json:"host"`
	Port                int                 `json:"port"`
	RecordDir           string              `json:"record_dir"`
	SampleRate          int                 `json:"sample_rate"`
	MP3Bitrate          string              `json:"mp3_bitrate"`
	ASREnabled          bool                `json:"asr_enabled"`
	ASRModel            string              `json:"asr_model"`
	ASRLanguage         string              `json:"asr_language"`
	ASRThreads          int                 `json:"asr_threads"`
	ASRIdleUnloadMin    int                 `json:"asr_idle_unload_min"`
	ASRVAD              bool                `json:"asr_vad"`
	ASRPrompt           string              `json:"asr_prompt"`
	HallucinationPhrase map[string][]string `json:"hallucination_phrases"`
	NoSpeechThreshold   float64             `json:"no_speech_threshold"`
	Dictionary          []DictEntry         `json:"dictionary"`
	RetentionDays       int                 `json:"retention_days"`
	FFmpegPath          string              `json:"ffmpeg_path"`
	WhisperServerPath   string              `json:"whisper_server_path"`
	UILanguage          string              `json:"ui_language"`
	Receivers           []Receiver          `json:"receivers"`
	Notify              Notify              `json:"notify"`
}

// Store persists a Config to disk with a mutex.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// DefaultHallucinations are the built-in hallucination phrases per language.
var DefaultHallucinations = map[string][]string{
	"ja": {
		"ご視聴ありがとうございました", "ご視聴ありがとうございます", "チャンネル登録お願いします",
		"最後までご視聴いただきありがとうございました", "おやすみなさい", "ありがとうございました", "字幕", "by H.",
	},
	"en": {
		"Thank you for watching", "Thanks for watching", "Please subscribe", "Subtitles by",
		"Transcribed by", "Thank you.", "you",
	},
}

// DefaultPrompts are the default ASR hint texts per language.
var DefaultPrompts = map[string][]string{
	"ja": {"無線交信の文字起こし。了解、どうぞ、こちら、送れ、以上。"},
	"en": {"Radio communication transcript. Roger, copy, over, out, go ahead."},
}

// Default returns a fresh default configuration.
func Default() Config {
	threads := runtime.NumCPU() - 1
	if threads < 1 {
		threads = 1
	}
	hp := map[string][]string{}
	for k, v := range DefaultHallucinations {
		hp[k] = append([]string{}, v...)
	}
	return Config{
		Host:                "0.0.0.0",
		Port:                8000,
		RecordDir:           "recordings",
		SampleRate:          16000,
		MP3Bitrate:          "48k",
		ASREnabled:          true,
		ASRModel:            "small-q5_1",
		ASRLanguage:         "ja",
		ASRThreads:          threads,
		ASRIdleUnloadMin:    30,
		ASRPrompt:           "",
		HallucinationPhrase: hp,
		NoSpeechThreshold:   0.7,
		Dictionary:          []DictEntry{},
		RetentionDays:       0,
		UILanguage:          "auto",
		Receivers:           []Receiver{},
		Notify: Notify{
			Enabled:         false,
			Webhooks:        []Webhook{},
			RateLimitPerMin: 20,
			MinDurationSec:  1.0,
		},
	}
}

// DefaultReceiver returns a receiver with sensible VOX defaults.
func DefaultReceiver() Receiver {
	return Receiver{
		SourceType:     "device",
		ChannelSelect:  "mono",
		Enabled:        true,
		VoxThresholdDB: -35,
		SilenceSec:     1.5,
		PrebufferSec:   0.5,
		MinCallSec:     0.7,
		MaxCallSec:     120,
		ASREnabled:     true,
		ASRPriority:    0,
		Color:          "#3b82f6",
	}
}

// Load reads config.json at path, creating it with defaults when absent.
func Load(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, fmt.Errorf("config.json: %w", err)
	}
	s.normalize()
	return s, nil
}

func (s *Store) normalize() {
	def := Default()
	if s.cfg.Port == 0 {
		s.cfg.Port = def.Port
	}
	if s.cfg.SampleRate == 0 {
		s.cfg.SampleRate = def.SampleRate
	}
	if s.cfg.MP3Bitrate == "" {
		s.cfg.MP3Bitrate = def.MP3Bitrate
	}
	if s.cfg.RecordDir == "" {
		s.cfg.RecordDir = def.RecordDir
	}
	if s.cfg.ASRThreads <= 0 {
		s.cfg.ASRThreads = def.ASRThreads
	}
	if s.cfg.ASRModel == "" {
		s.cfg.ASRModel = def.ASRModel
	}
	if s.cfg.ASRLanguage == "" {
		s.cfg.ASRLanguage = def.ASRLanguage
	}
	if s.cfg.NoSpeechThreshold == 0 {
		s.cfg.NoSpeechThreshold = def.NoSpeechThreshold
	}
	if s.cfg.UILanguage == "" {
		s.cfg.UILanguage = "auto"
	}
	if s.cfg.HallucinationPhrase == nil {
		s.cfg.HallucinationPhrase = def.HallucinationPhrase
	}
	if s.cfg.Dictionary == nil {
		s.cfg.Dictionary = []DictEntry{}
	}
	if s.cfg.Receivers == nil {
		s.cfg.Receivers = []Receiver{}
	}
	if s.cfg.Notify.Webhooks == nil {
		s.cfg.Notify.Webhooks = []Webhook{}
	}
	if s.cfg.Notify.RateLimitPerMin <= 0 {
		s.cfg.Notify.RateLimitPerMin = 20
	}
	for i := range s.cfg.Receivers {
		r := &s.cfg.Receivers[i]
		if r.SourceType == "" {
			r.SourceType = "device"
		}
		if r.ChannelSelect == "" {
			r.ChannelSelect = "mono"
		}
		if r.MaxCallSec <= 0 {
			r.MaxCallSec = 120
		}
		if r.Color == "" {
			r.Color = "#3b82f6"
		}
	}
	for i := range s.cfg.Notify.Webhooks {
		w := &s.cfg.Notify.Webhooks[i]
		if w.Timing == "" {
			w.Timing = "on_transcript"
		}
		if w.KeywordMode == "" {
			w.KeywordMode = "any"
		}
		if w.Type == "" {
			w.Type = "discord"
		}
		if w.ReceiverIDs == nil {
			w.ReceiverIDs = []string{}
		}
		if w.Keywords == nil {
			w.Keywords = []string{}
		}
	}
}

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

// Update applies fn under lock and saves. fn may return an error to abort.
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.cfg)
	if err := fn(&next); err != nil {
		return err
	}
	s.cfg = next
	s.normalize()
	return s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil && filepath.Dir(s.path) != "." {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func clone(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}

// FindReceiver returns the receiver with id or nil.
func (c *Config) FindReceiver(id string) *Receiver {
	for i := range c.Receivers {
		if c.Receivers[i].ID == id {
			return &c.Receivers[i]
		}
	}
	return nil
}

// FindWebhook returns the webhook with id or nil.
func (c *Config) FindWebhook(id string) *Webhook {
	for i := range c.Notify.Webhooks {
		if c.Notify.Webhooks[i].ID == id {
			return &c.Notify.Webhooks[i]
		}
	}
	return nil
}

// Hallucinations returns the phrase list for lang, falling back to en.
func (c *Config) Hallucinations(lang string) []string {
	if v, ok := c.HallucinationPhrase[lang]; ok {
		return v
	}
	if v, ok := c.HallucinationPhrase["en"]; ok {
		return v
	}
	return DefaultHallucinations["en"]
}

// EffectivePrompt returns the ASR hint text for the configured language.
func (c *Config) EffectivePrompt() string {
	if c.ASRPrompt != "" {
		return c.ASRPrompt
	}
	if p, ok := DefaultPrompts[c.ASRLanguage]; ok {
		return p[0]
	}
	return DefaultPrompts["en"][0]
}

// NotifyLanguage returns the language used for notification templates.
func (c *Config) NotifyLanguage() string {
	if c.Notify.Language != "" {
		return c.Notify.Language
	}
	if c.UILanguage == "" || c.UILanguage == "auto" {
		return "ja"
	}
	return c.UILanguage
}
