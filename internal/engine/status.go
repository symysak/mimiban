package engine

import (
	"os"
	"path/filepath"
	"time"

	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/capture"
)

// ReceiverStatus is the live state of one receiver.
type ReceiverStatus struct {
	State        string  `json:"state"`
	LevelDB      float64 `json:"level_db"`
	CallElapsed  float64 `json:"call_elapsed"`
	Error        string  `json:"error"`
	LastPacketAt string  `json:"last_packet_at,omitempty"`
	Pending      int     `json:"pending"`
}

// ASRStatus is the ASR part of /api/status.
type ASRStatus struct {
	Enabled      bool             `json:"enabled"`
	Server       asr.ServerStatus `json:"server"`
	PendingTotal int              `json:"pending_total"`
	RunningID    int64            `json:"running_id"`
	LastError    string           `json:"last_error"`
}

// Status is the /api/status payload.
type Status struct {
	Receivers map[string]ReceiverStatus `json:"receivers"`
	ASR       ASRStatus                 `json:"asr"`
	FFmpeg    capture.Binary            `json:"ffmpeg"`
	Whisper   capture.Binary            `json:"whisper_server"`
	SRT       bool                      `json:"srt"`
	Now       string                    `json:"now"`
}

// Status builds the snapshot. It must stay cheap (250 ms polling).
func (e *Engine) Status() Status {
	cfg := e.o.Config.Get()
	st := Status{Receivers: map[string]ReceiverStatus{}, FFmpeg: e.ffmpeg, Whisper: e.whisper, SRT: e.srt,
		Now: e.o.Now().Format(time.RFC3339)}
	e.mu.Lock()
	snaps := map[string]ReceiverStatus{}
	for id, w := range e.workers {
		snaps[id] = w.snapshot()
	}
	e.mu.Unlock()
	pend, total, _ := e.o.DB.PendingCounts()
	for _, r := range cfg.Receivers {
		s, ok := snaps[r.ID]
		if !ok {
			s = ReceiverStatus{State: StStopped, LevelDB: -100}
		}
		s.Pending = pend[r.ID]
		st.Receivers[r.ID] = s
	}
	st.ASR.Enabled = cfg.ASREnabled
	if e.o.Server != nil {
		st.ASR.Server = e.o.Server.Status()
		if st.ASR.Server.Model == "" {
			st.ASR.Server.Model = cfg.ASRModel
		} else {
			st.ASR.Server.Model = filepath.Base(st.ASR.Server.Model)
		}
	} else {
		st.ASR.Server = asr.ServerStatus{State: asr.SrvStopped, Model: cfg.ASRModel}
	}
	st.ASR.PendingTotal = total
	e.asrState.mu.Lock()
	st.ASR.RunningID = e.asrState.runningID
	st.ASR.LastError = e.asrState.lastError
	e.asrState.mu.Unlock()
	return st
}

// DiskUsage walks the recordings directory.
type DiskUsage struct {
	UsedBytes  int64  `json:"used_bytes"`
	Files      int    `json:"files"`
	FreeBytes  int64  `json:"free_bytes"`
	TotalBytes int64  `json:"total_bytes"`
	Dir        string `json:"dir"`
}

// Disk computes usage of the recordings directory.
func (e *Engine) Disk() DiskUsage {
	dir := e.RecordDir()
	du := DiskUsage{Dir: dir}
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			du.UsedBytes += info.Size()
			du.Files++
		}
		return nil
	})
	// recordings/ is created lazily on the first call, so fall back to the
	// nearest existing ancestor; statfs / GetDiskFreeSpaceEx fail otherwise.
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	du.FreeBytes, du.TotalBytes = diskFree(probe)
	return du
}
