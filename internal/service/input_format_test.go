package service

import (
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Compatibility is decided by the verified format alone: CSV feeds general
// training, JSON feeds finetuning. An unrecognized value must be rejected,
// because matching nothing would look like an empty tenant.
func TestInputQueryFilterAcceptsOnlyVerifiedFormats(t *testing.T) {
	text := func(value string) *string { return &value }
	state := func(value modeldevv1.InputState) *modeldevv1.InputState { return &value }
	for _, test := range []struct {
		name       string
		in         *modeldevv1.ListInputVersionsRequest
		wantErr    bool
		wantFormat string
		wantState  biz.InputState
	}{
		{name: "no selectors keeps everything", in: &modeldevv1.ListInputVersionsRequest{}},
		{name: "CSV", in: &modeldevv1.ListInputVersionsRequest{Format: text("CSV")}, wantFormat: "CSV"},
		{name: "JSON", in: &modeldevv1.ListInputVersionsRequest{Format: text("JSON")}, wantFormat: "JSON"},
		{name: "lower case rejected", in: &modeldevv1.ListInputVersionsRequest{Format: text("csv")}, wantErr: true},
		{name: "unknown format rejected", in: &modeldevv1.ListInputVersionsRequest{Format: text("PARQUET")}, wantErr: true},
		{name: "empty value rejected", in: &modeldevv1.ListInputVersionsRequest{Format: text("")}, wantErr: true},
		{name: "format and state combine",
			in:      &modeldevv1.ListInputVersionsRequest{Format: text("JSON"), State: state(modeldevv1.InputState_INPUT_STATE_READY)},
			wantFormat: "JSON", wantState: biz.InputStateReady},
		{name: "ready state", in: &modeldevv1.ListInputVersionsRequest{State: state(modeldevv1.InputState_INPUT_STATE_READY)}, wantState: biz.InputStateReady},
		{name: "validating state", in: &modeldevv1.ListInputVersionsRequest{State: state(modeldevv1.InputState_INPUT_STATE_VALIDATING)}, wantState: biz.InputStateValidating},
		{name: "failed state maps to rejected", in: &modeldevv1.ListInputVersionsRequest{State: state(modeldevv1.InputState_INPUT_STATE_FAILED)}, wantState: biz.InputStateRejected},
		{name: "unspecified state rejected", in: &modeldevv1.ListInputVersionsRequest{State: state(modeldevv1.InputState_INPUT_STATE_UNSPECIFIED)}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := inputQueryFilter(test.in)
			if test.wantErr {
				if err == nil {
					t.Fatalf("accepted %+v", test.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected a valid request: %v", err)
			}
			if got.Format != test.wantFormat {
				t.Fatalf("format=%q, want %q", got.Format, test.wantFormat)
			}
			if got.State != test.wantState {
				t.Fatalf("state=%q, want %q", got.State, test.wantState)
			}
		})
	}
}
