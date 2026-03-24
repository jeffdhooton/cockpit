package sources

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CalendarEvent represents an upcoming calendar event.
type CalendarEvent struct {
	Summary    string
	MinutesOut int
}

// GetUpcomingEvents returns calendar events in the next N minutes via osascript.
func GetUpcomingEvents(ctx context.Context, lookaheadMinutes int) ([]CalendarEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	script := fmt.Sprintf(`
set now to current date
set later to now + %d * 60
set eventList to {}
tell application "Calendar"
    repeat with cal in calendars
        try
            set calEvents to (every event of cal whose start date > now and start date < later)
            repeat with evt in calEvents
                set mins to ((start date of evt) - now) div 60
                set end of eventList to (summary of evt) & "|||" & (mins as text)
            end repeat
        end try
    end repeat
end tell
set text item delimiters to "|||SEP|||"
return eventList as text
`, lookaheadMinutes)

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}

	var events []CalendarEvent
	for _, entry := range strings.Split(raw, "|||SEP|||") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "|||", 2)
		if len(parts) != 2 {
			continue
		}
		mins := 0
		fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &mins)
		events = append(events, CalendarEvent{
			Summary:    strings.TrimSpace(parts[0]),
			MinutesOut: mins,
		})
	}
	return events, nil
}
