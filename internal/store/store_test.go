package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	d, err := Open(filepath.Join(t.TempDir(), "t.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func ins(t *testing.T, d *DB, rx string, at time.Time, dur float64, text string) int64 {
	c := &Call{ReceiverID: rx, Channel: rx, StartedAt: FormatTime(at), Duration: dur, Path: "/tmp/x.mp3", ASRStatus: "pending"}
	id, err := d.InsertCall(c)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		if err := d.UpdateTranscript(id, text, "done", nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestListFilters(t *testing.T) {
	d := openTest(t)
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.Local)
	ins(t, d, "a", base, 3, "fire at the station")
	ins(t, d, "b", base.Add(time.Hour), 4, "ambulance dispatched")
	ins(t, d, "a", base.AddDate(0, 0, 1), 5, "100% sure_thing")
	items, total, err := d.ListCalls(ListFilter{})
	if err != nil || total != 3 || len(items) != 3 || items[0].Duration != 5 {
		t.Fatalf("all: %v %d %d", err, total, len(items))
	}
	_, total, _ = d.ListCalls(ListFilter{Date: "2026-09-14"})
	if total != 2 {
		t.Fatalf("date: %d", total)
	}
	_, total, _ = d.ListCalls(ListFilter{ReceiverIDs: []string{"b"}})
	if total != 1 {
		t.Fatalf("rx: %d", total)
	}
	_, total, _ = d.ListCalls(ListFilter{Query: "fire"})
	if total != 1 {
		t.Fatalf("q: %d", total)
	}
	_, total, _ = d.ListCalls(ListFilter{Query: "%"})
	if total != 1 {
		t.Fatalf("escaped %%: %d", total)
	}
	_, total, _ = d.ListCalls(ListFilter{Query: "_thing"})
	if total != 1 {
		t.Fatalf("escaped _: %d", total)
	}
	items, total, _ = d.ListCalls(ListFilter{ReceiverIDs: []string{"a", "b"}, Query: "a", Limit: 1, Offset: 1})
	if total != 2 || len(items) != 1 {
		t.Fatalf("cross-receiver paging: %d %d", total, len(items))
	}
}

func TestPendingAndStatus(t *testing.T) {
	d := openTest(t)
	now := time.Now()
	id1 := ins(t, d, "a", now, 3, "")
	id2 := ins(t, d, "b", now, 3, "")
	if _, err := d.db.Exec(`UPDATE calls SET priority=5 WHERE id=?`, id2); err != nil {
		t.Fatal(err)
	}
	c, _ := d.NextPending()
	if c == nil || c.ID != id2 {
		t.Fatalf("priority order: %+v", c)
	}
	m, total, _ := d.PendingCounts()
	if total != 2 || m["a"] != 1 {
		t.Fatalf("pending counts %v %d", m, total)
	}
	_ = d.UpdateTranscript(id2, "x", "done", nil, nil, nil)
	c, _ = d.NextPending()
	if c == nil || c.ID != id1 {
		t.Fatal("next pending")
	}
	e := "boom"
	_ = d.SetStatus(id1, "error", &e)
	n, _ := d.ResetStatus("pending", 0)
	if n != 1 {
		t.Fatalf("reset pending: %d", n)
	}
	n, _ = d.ResetStatus("all", 0)
	if n != 2 {
		t.Fatalf("reset all: %d", n)
	}
	p, err := d.DeleteCall(id1)
	if err != nil || p != "/tmp/x.mp3" {
		t.Fatal("delete")
	}
	if _, err := d.GetCall(id1); err == nil {
		t.Fatal("should be gone")
	}
}

func TestStats(t *testing.T) {
	d := openTest(t)
	now := time.Date(2026, 9, 15, 15, 30, 0, 0, time.Local) // Tuesday
	ins(t, d, "a", now.Add(-time.Hour), 10, "")
	ins(t, d, "a", now.Add(-2*time.Hour), 20, "")
	ins(t, d, "b", now.AddDate(0, 0, -3), 30, "")
	ins(t, d, "b", now.AddDate(0, 0, -10), 40, "") // outside 7 days
	st, err := d.Stats(7, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 3 || st.TotalDuration != 60 || st.AvgDuration != 20 {
		t.Fatalf("totals: %+v", st)
	}
	if st.ByHour[14].Count != 1 || st.ByHour[13].Count != 1 {
		t.Fatalf("by hour: %+v", st.ByHour)
	}
	if st.ByWeekday[int(now.Weekday())].Count != 2 {
		t.Fatalf("by weekday: %+v", st.ByWeekday)
	}
	if len(st.ByDay) != 7 || st.ByDay[6].Count != 2 || st.ByDay[3].Count != 1 {
		t.Fatalf("by day: %+v", st.ByDay)
	}
	if len(st.ByReceiver) != 2 {
		t.Fatalf("by rx: %+v", st.ByReceiver)
	}
	st, _ = d.Stats(30, []string{"b"}, now)
	if st.Count != 2 || st.TotalDuration != 70 {
		t.Fatalf("rx filter 30d: %+v", st)
	}
}

func TestNotifyLog(t *testing.T) {
	d := openTest(t)
	code := 200
	for i := 0; i < 60; i++ {
		_ = d.InsertNotifyLog(NotifyLog{WebhookID: "w", SentAt: FormatTime(time.Now()), Status: "sent", HTTPStatus: &code})
	}
	rows, err := d.RecentNotifyLog(50)
	if err != nil || len(rows) != 50 || rows[0].ID != 60 {
		t.Fatalf("%v %d", err, len(rows))
	}
}
