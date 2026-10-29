package biz

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// The kind filter reads the frozen snapshot kind, which is the same value the
// detail response reports. It must narrow the list without disturbing the
// state and name filters, and an empty kind must keep every row.
func TestExecutionQueryMatchesKind(t *testing.T) {
	record := func(kind string) Execution {
		return Execution{
			Admission: Admission{Intent: cpup01.Intent{Name: "task"}, Snapshot: cpup01.Snapshot{Kind: kind}},
			States:    ExecutionStates{Compute: "SUCCEEDED"},
		}
	}
	for _, test := range []struct {
		name  string
		query ExecutionQuery
		entry Execution
		want  bool
	}{
		{name: "empty filter keeps everything", query: ExecutionQuery{}, entry: record("FINETUNING"), want: true},
		{name: "finetuning matches finetuning", query: ExecutionQuery{Kind: "FINETUNING"}, entry: record("FINETUNING"), want: true},
		{name: "finetuning rejects general training", query: ExecutionQuery{Kind: "FINETUNING"}, entry: record("GENERAL_TRAINING"), want: false},
		{name: "general training matches general training", query: ExecutionQuery{Kind: "GENERAL_TRAINING"}, entry: record("GENERAL_TRAINING"), want: true},
		{name: "kind and state must both match", query: ExecutionQuery{Kind: "FINETUNING", Compute: "FAILED"}, entry: record("FINETUNING"), want: false},
		{name: "kind and state both match", query: ExecutionQuery{Kind: "FINETUNING", Compute: "SUCCEEDED"}, entry: record("FINETUNING"), want: true},
		{name: "kind with name both match", query: ExecutionQuery{Kind: "FINETUNING", Name: "tas"}, entry: record("FINETUNING"), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.query.Matches(test.entry); got != test.want {
				t.Fatalf("Matches()=%v, want %v", got, test.want)
			}
		})
	}
}
