package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStepPublicationFailureDiagnosticIsBoundedAndPreservesPriority(t *testing.T) {
	const secret = "credential-value https://private.invalid/object?token=signed-secret"
	_, publication := workspace.UploadOutput(context.Background(), s3.New(s3.Options{}), biz.Execution{}, t.TempDir(), biz.CollectedOutput{})
	if !errors.Is(publication, biz.ErrInvalidWorkspaceOutput) {
		t.Fatal("publisher lost the original failure identity")
	}
	wrapped := fmt.Errorf("%s: %w", secret, publication)
	for _, test := range []struct {
		name, want string
		cause      error
	}{
		{name: "publication", cause: wrapped, want: "managed component failed stage=run class=publication code=Unknown phase=validate reason=manifest http_status=0 storage_code=none"},
		{name: "configuration priority", cause: errors.Join(wrapped, component.ErrConfiguration), want: "managed component failed stage=run class=configuration code=Unknown"},
		{name: "cancel priority", cause: errors.Join(wrapped, context.Canceled), want: "managed component failed stage=run class=canceled code=Unknown"},
		{name: "deadline priority", cause: errors.Join(wrapped, context.DeadlineExceeded), want: "managed component failed stage=run class=deadline code=Unknown"},
		{name: "grpc priority", cause: errors.Join(wrapped, status.Error(codes.Unavailable, secret)), want: "managed component failed stage=run class=grpc code=Unavailable"},
		{name: "untrusted lookalike", cause: publicationDiagnosticLookalike{}, want: "managed component failed stage=run class=local code=Unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := stageFailure("run", test.cause)
			if !errors.Is(err, test.cause) {
				t.Fatal("CLI wrapper lost its original error chain")
			}
			line := failureDiagnostic(err)
			if line != test.want {
				t.Fatalf("wrong bounded diagnostic: %q", line)
			}
			for _, token := range []string{"credential-value", "https://", "signed-secret", "untrusted-phase"} {
				if strings.Contains(line, token) {
					t.Fatal("CLI leaked untrusted error details")
				}
			}
		})
	}
}

type publicationDiagnosticLookalike struct{}

func (publicationDiagnosticLookalike) Error() string { return "credential-value" }
func (publicationDiagnosticLookalike) PublicationDiagnostic() (string, string, int, string) {
	return "untrusted-phase", "credential-value", 999999, "signed-secret"
}
