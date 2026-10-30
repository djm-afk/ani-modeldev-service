package imageruntime

import (
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A definitive catalogue answer rejects the plan; only availability-class
// failures remain retryable.
func TestResolveFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"image not visible", status.Error(codes.NotFound, "IMAGE_NOT_FOUND"), biz.ErrImplementationUnavailable},
		{"bad selection", status.Error(codes.InvalidArgument, "bad"), biz.ErrImplementationUnavailable},
		{"unsupported platform", status.Error(codes.FailedPrecondition, "platform"), biz.ErrImplementationUnavailable},
		{"unavailable", status.Error(codes.Unavailable, "down"), nil},
		{"deadline", status.Error(codes.DeadlineExceeded, "slow"), nil},
		{"plain", errors.New("boom"), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolveFailure(test.err)
			if test.want == nil {
				if got != test.err {
					t.Fatalf("transport failure rewritten: %v", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}
