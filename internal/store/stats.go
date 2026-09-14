package store

import (
	"time"
)

// Bucket is one bar of a chart.
type Bucket struct {
	Key      string  `json:"key"`
	Count    int     `json:"count"`
	Duration float64 `json:"duration"`
}

// Stats is the payload of /api/stats.
type Stats struct {
	Days          int      `json:"days"`
	Count         int      `json:"count"`
	TotalDuration float64  `json:"total_duration"`
	AvgDuration   float64  `json:"avg_duration"`
	ByHour        []Bucket `json:"by_hour"`    // 24
	ByWeekday     []Bucket `json:"by_weekday"` // 7, 0=Sunday
	ByDay         []Bucket `json:"by_day"`
	ByReceiver    []Bucket `json:"by_receiver"`
}

// Stats aggregates calls in the last days for the given receivers (nil = all).
func (d *DB) Stats(days int, receiverIDs []string, now time.Time) (*Stats, error) {
	if days <= 0 {
		days = 7
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	f := ListFilter{ReceiverIDs: receiverIDs}
	where, args := buildWhere(f)
	if where == "" {
		where = " WHERE started_at >= ?"
	} else {
		where += " AND started_at >= ?"
	}
	args = append(args, FormatTime(start))
	rows, err := d.db.Query(`SELECT receiver_id, channel, started_at, duration FROM calls`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	st := &Stats{Days: days}
	st.ByHour = make([]Bucket, 24)
	for i := range st.ByHour {
		st.ByHour[i].Key = itoa(i)
	}
	st.ByWeekday = make([]Bucket, 7)
	for i := range st.ByWeekday {
		st.ByWeekday[i].Key = itoa(i)
	}
	dayIdx := map[string]int{}
	for i := 0; i < days; i++ {
		k := start.AddDate(0, 0, i).Format("2006-01-02")
		dayIdx[k] = i
		st.ByDay = append(st.ByDay, Bucket{Key: k})
	}
	rxIdx := map[string]int{}
	for rows.Next() {
		var rx, ch, ts string
		var dur float64
		if err := rows.Scan(&rx, &ch, &ts, &dur); err != nil {
			return nil, err
		}
		t, err := time.Parse("2006-01-02T15:04:05.000-07:00", ts)
		if err != nil {
			t, err = time.Parse(time.RFC3339, ts)
			if err != nil {
				continue
			}
		}
		t = t.In(now.Location())
		st.Count++
		st.TotalDuration += dur
		add(&st.ByHour[t.Hour()], dur)
		add(&st.ByWeekday[int(t.Weekday())], dur)
		if i, ok := dayIdx[t.Format("2006-01-02")]; ok {
			add(&st.ByDay[i], dur)
		}
		i, ok := rxIdx[rx]
		if !ok {
			i = len(st.ByReceiver)
			rxIdx[rx] = i
			st.ByReceiver = append(st.ByReceiver, Bucket{Key: rx})
		}
		add(&st.ByReceiver[i], dur)
	}
	if st.Count > 0 {
		st.AvgDuration = st.TotalDuration / float64(st.Count)
	}
	if st.ByReceiver == nil {
		st.ByReceiver = []Bucket{}
	}
	return st, rows.Err()
}

func add(b *Bucket, dur float64) {
	b.Count++
	b.Duration += dur
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
