package storagecredentials

import (
	"testing"
	"time"
)

// The session must cover the attempt's remaining window, not a fixed 15 minutes.
// A fixed duration made every import longer than that fail at upload time.
func TestImportSessionSecondsCoversTheDeadline(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		deadline time.Time
		want     int64
	}{
		{"short window keeps the STS floor", now.Add(5 * time.Minute), 900},
		{"exactly the floor", now.Add(15 * time.Minute), 900},
		{"a long import gets its full window", now.Add(2 * time.Hour), 7200},
		{"clamped to the AssumeRole ceiling", now.Add(30 * time.Hour), maxImportSessionSeconds},
	}
	for _, testCase := range cases {
		if got := importSessionSeconds(testCase.deadline, now); got != testCase.want {
			t.Fatalf("%s: got %d, want %d", testCase.name, got, testCase.want)
		}
	}
}
