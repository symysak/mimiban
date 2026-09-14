package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/symysak/mimiban/internal/store"
)

func (s *Server) calls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{Date: q.Get("date"), Query: q.Get("q")}
	if v := q.Get("receiver_id"); v != "" {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				f.ReceiverIDs = append(f.ReceiverIDs, id)
			}
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	items, total, err := s.DB.ListCalls(f)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": items, "limit": f.Limit, "offset": f.Offset})
}

func parseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("bad_id")
	}
	return id, nil
}

func (s *Server) callGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.DB.GetCall(id)
	if err != nil {
		writeErr(w, 404, errors.New("call_not_found"))
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) callAudio(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.DB.GetCall(id)
	if err != nil {
		writeErr(w, 404, errors.New("call_not_found"))
		return
	}
	f, err := os.Open(c.Path)
	if err != nil {
		writeErr(w, 404, errors.New("audio_missing"))
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	ct := "audio/mpeg"
	if strings.HasSuffix(strings.ToLower(c.Path), ".wav") {
		ct = "audio/wav"
	}
	w.Header().Set("Content-Type", ct)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(c.Path)+`"`)
	}
	http.ServeContent(w, r, filepath.Base(c.Path), st.ModTime(), f)
}

func (s *Server) callTranscript(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var in struct {
		Transcript string `json:"transcript"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, errors.New("bad_json"))
		return
	}
	c, err := s.DB.GetCall(id)
	if err != nil {
		writeErr(w, 404, errors.New("call_not_found"))
		return
	}
	model := "manual"
	if c.ASRModel != nil && *c.ASRModel != "" {
		model = *c.ASRModel
	}
	if err := s.DB.UpdateTranscript(id, in.Transcript, "done", &model, nil, nil); err != nil {
		writeErr(w, 500, err)
		return
	}
	c, _ = s.DB.GetCall(id)
	writeJSON(w, 200, c)
}

func (s *Server) callTranscribe(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if _, err := s.Engine.Retranscribe("one", id); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) callDelete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.Engine.DeleteCall(id); err != nil {
		writeErr(w, 404, errors.New("call_not_found"))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) transcribePending(w http.ResponseWriter, _ *http.Request) {
	n, err := s.Engine.Retranscribe("pending", 0)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"count": n})
}

func (s *Server) transcribeAll(w http.ResponseWriter, _ *http.Request) {
	n, err := s.Engine.Retranscribe("all", 0)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"count": n})
}

func (s *Server) dictionaryApply(w http.ResponseWriter, _ *http.Request) {
	n, err := s.Engine.ApplyDictionaryAll()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]int{"count": n})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	var ids []string
	if v := r.URL.Query().Get("receiver_id"); v != "" {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	st, err := s.DB.Stats(days, ids, time.Now())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) disk(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Engine.Disk())
}
