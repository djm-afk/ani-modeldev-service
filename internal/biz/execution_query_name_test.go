package biz

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// The name filter is a case-insensitive substring over the name the list
// reports. It must combine with the state filters instead of replacing them,
// and an empty filter must keep every row.
func TestExecutionQueryMatchesName(t *testing.T) {
	record := func(name string, compute ComputeState) Execution {
		return Execution{
			Admission: Admission{Intent: cpup01.Intent{Name: name}},
			States:    ExecutionStates{Compute: compute},
		}
	}
	for _, test := range []struct {
		name  string
		query ExecutionQuery
		entry Execution
		want  bool
	}{
		{name: "empty filter keeps everything", query: ExecutionQuery{}, entry: record("lora-sft-01", "SUCCEEDED"), want: true},
		{name: "exact match", query: ExecutionQuery{Name: "lora-sft-01"}, entry: record("lora-sft-01", "SUCCEEDED"), want: true},
		{name: "substring match", query: ExecutionQuery{Name: "sft"}, entry: record("lora-sft-01", "SUCCEEDED"), want: true},
		{name: "folded query matches lower name", query: ExecutionQuery{Name: "lora"}, entry: record("lora-sft-01", "SUCCEEDED"), want: true},
		{name: "stored upper case still matches", query: ExecutionQuery{Name: "lora"}, entry: record("LORA-SFT-01", "SUCCEEDED"), want: true},
		{name: "no substring", query: ExecutionQuery{Name: "dpo"}, entry: record("lora-sft-01", "SUCCEEDED"), want: false},
		{name: "name and state must both match", query: ExecutionQuery{Name: "lora", Compute: "FAILED"}, entry: record("lora-sft-01", "SUCCEEDED"), want: false},
		{name: "name and state both match", query: ExecutionQuery{Name: "lora", Compute: "SUCCEEDED"}, entry: record("lora-sft-01", "SUCCEEDED"), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.query.Matches(test.entry); got != test.want {
				t.Fatalf("Matches()=%v, want %v", got, test.want)
			}
		})
	}
}
