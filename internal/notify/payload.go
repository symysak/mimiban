package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const discordMaxDesc = 2000

// DiscordPayload builds the webhook JSON body (one embed).
func DiscordPayload(j Job, tm Templates, link string, more int) map[string]any {
	c := j.Call
	title := j.Receiver.Name
	if title == "" && c != nil {
		title = c.Channel
	}
	desc := ""
	if j.Event == "test" {
		title = tm.TestTitle
		desc = tm.TestBody
	} else if j.Event == "call" {
		desc = tm.Transcribing
	} else if c != nil && c.Transcript != nil {
		desc = *c.Transcript
	}
	if desc == "" && j.Event == "transcript" {
		desc = "…"
	}
	if len([]rune(desc)) > discordMaxDesc-len([]rune(tm.Truncated))-10 {
		r := []rune(desc)
		desc = string(r[:discordMaxDesc-len([]rune(tm.Truncated))-10]) + "\n" + tm.Truncated
	}
	embed := map[string]any{
		"title":       title,
		"description": desc,
		"color":       colorInt(j.Receiver.Color),
	}
	if link != "" && c != nil && c.ID > 0 {
		embed["url"] = link
	}
	if c != nil {
		fields := []map[string]any{
			{"name": tm.FieldTime, "value": humanTime(c.StartedAt), "inline": true},
			{"name": tm.FieldLength, "value": fmt.Sprintf(tm.Seconds, c.Duration), "inline": true},
		}
		if c.PeakDB != nil {
			fields = append(fields, map[string]any{"name": tm.FieldPeak, "value": fmt.Sprintf("%.1f dB", *c.PeakDB), "inline": true})
		}
		embed["fields"] = fields
		embed["timestamp"] = toRFC3339(c.StartedAt)
	}
	if more > 0 {
		embed["footer"] = map[string]any{"text": strings.ReplaceAll(tm.MoreCalls, "{n}", strconv.Itoa(more))}
	}
	content := ""
	if j.Webhook.Mention != "" {
		content = j.Webhook.Mention
	}
	if j.Event != "test" && link != "" && c != nil && c.ID > 0 && !j.Webhook.AttachAudio {
		// Discord does not render embed links on mobile titles reliably; add a
		// plain link line too.
		if content != "" {
			content += " "
		}
		content += tm.OpenInUI + " " + link
	}
	payload := map[string]any{
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{"everyone", "roles", "users"}},
	}
	if content != "" {
		payload["content"] = content
	}
	return payload
}

// GenericPayload builds the generic_json body.
func GenericPayload(j Job, link string) map[string]any {
	ev := j.Event
	if ev == "test" {
		ev = "call"
	}
	audio := ""
	if j.Call != nil && j.Call.ID > 0 {
		base := link
		if i := strings.Index(base, "/#/"); i >= 0 {
			base = base[:i]
		}
		audio = strings.TrimRight(base, "/") + "/api/calls/" + strconv.FormatInt(j.Call.ID, 10) + "/audio"
	}
	return map[string]any{
		"event":     ev,
		"call":      j.Call,
		"receiver":  map[string]any{"id": j.Receiver.ID, "name": j.Receiver.Name},
		"audio_url": audio,
		"url":       link,
	}
}

func colorInt(hex string) int {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0x3b82f6
	}
	v, err := strconv.ParseInt(hex, 16, 32)
	if err != nil {
		return 0x3b82f6
	}
	return int(v)
}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05.000-07:00", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func humanTime(s string) string {
	if t, ok := parseTime(s); ok {
		return t.Format("2006-01-02 15:04:05")
	}
	return s
}

func toRFC3339(s string) string {
	if t, ok := parseTime(s); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}
