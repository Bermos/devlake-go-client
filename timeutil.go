package devlake

import (
	"fmt"
	"time"
)

// timeLayouts are the formats DevLake uses when serializing timestamps.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05-0700",
	"2006-01-02T15:04:05.999999999-0700",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// ParseTime parses a timestamp string as returned by DevLake. Generated models
// keep timestamps as strings because DevLake mixes RFC 3339 with its own
// ISO 8601 variant (no colon in the offset).
func ParseTime(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("devlake: unrecognized time %q", s)
}
