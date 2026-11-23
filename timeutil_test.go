package devlake_test

import (
	"testing"
	"time"

	devlake "github.com/Bermos/devlake-go-client"
)

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	for _, s := range []string{
		"2026-11-23T09:30:00Z",
		"2026-11-23T09:30:00.000000Z",
		"2026-11-23T10:30:00+01:00",
		"2026-11-23T10:30:00+0100",
		"2026-11-23 09:30:00",
		"2026-11-23 09:30",
	} {
		got, err := devlake.ParseTime(s)
		if err != nil {
			t.Errorf("ParseTime(%q): %v", s, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("ParseTime(%q) = %v, want %v", s, got, want)
		}
	}
	if _, err := devlake.ParseTime("yesterday"); err == nil {
		t.Error("ParseTime accepted garbage")
	}
}
