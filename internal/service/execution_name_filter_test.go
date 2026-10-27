package service

import (
	"strings"
	"testing"
)

// The admitted name is 1-80 printable runes. A filter outside that shape can
// never match a stored row, so rejecting it keeps an impossible query from
// looking like an empty tenant.
func TestValidExecutionNameFilter(t *testing.T) {
	accepted := []string{"a", "lora-sft-01", "  padded  ", strings.Repeat("x", 80), "训练任务"}
	rejected := []string{"", "   ", strings.Repeat("x", 81), "bad\x00name", "line\nbreak", "tab\there"}
	for _, value := range accepted {
		if !validExecutionNameFilter(value) {
			t.Fatalf("rejected a usable filter %q", value)
		}
	}
	for _, value := range rejected {
		if validExecutionNameFilter(value) {
			t.Fatalf("accepted an unusable filter %q", value)
		}
	}
}
