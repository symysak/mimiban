package engine

import (
	"os"
	"path/filepath"
	"time"
)

// cleanupLoop deletes calls older than retention_days once an hour.
func (e *Engine) cleanupLoop() {
	defer e.wg.Done()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	e.Cleanup()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
			e.Cleanup()
		}
	}
}

// Cleanup runs one retention pass and returns the number of deleted calls.
func (e *Engine) Cleanup() int {
	cfg := e.o.Config.Get()
	if cfg.RetentionDays <= 0 {
		return 0
	}
	cutoff := e.o.Now().AddDate(0, 0, -cfg.RetentionDays)
	n := 0
	for {
		calls, err := e.o.DB.OlderThan(cutoff)
		if err != nil || len(calls) == 0 {
			break
		}
		for _, c := range calls {
			if err := e.DeleteCall(c.ID); err == nil {
				n++
			}
		}
	}
	if n > 0 {
		e.o.Logf("retention: deleted %d calls older than %d days", n, cfg.RetentionDays)
	}
	return n
}

// DeleteCall removes the DB row and the audio file (and empty parent dirs).
func (e *Engine) DeleteCall(id int64) error {
	p, err := e.o.DB.DeleteCall(id)
	if err != nil {
		return err
	}
	if p != "" {
		_ = os.Remove(p)
		dir := filepath.Dir(p)
		for i := 0; i < 2; i++ {
			if os.Remove(dir) != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return nil
}
