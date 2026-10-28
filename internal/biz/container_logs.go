package biz

import (
	"context"
	"io"

	"github.com/google/uuid"
)

// TrainerLogID is the single derivation of a training worker log ID. It keeps
// the historical SHA1-UUID form so existing stored and issued IDs stay valid.
func TrainerLogID(executionID, podUID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(executionID+":"+podUID+":node")).String()
}

// StepLogID is the opaque credential of one KFP step Pod. It is a separate
// derivation so a worker ID can never address a step Pod and vice versa.
func StepLogID(executionID, podUID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(executionID+":"+podUID+":step")).String()
}

// ContainerLogTarget is one already-authorized container instance. Pod and
// namespace identity come from a verified read; a caller cannot name another
// workload.
type ContainerLogTarget struct {
	Namespace, NamespaceUID string
	PodName, PodUID         string
	ContainerName           string
	Previous                bool
}

// ContainerLogReader reads one container instance bounded by lines and bytes.
type ContainerLogReader interface {
	ReadContainerLogs(context.Context, ContainerLogTarget, TrainingLogOptions) (TrainingLogResult, error)
}

// ContainerLogStreamer follows one container instance from its log head. The
// caller applies the resume offset; an unavailable or stale offset returns
// ErrLogOffsetInvalid.
type ContainerLogStreamer interface {
	StreamContainerLogs(context.Context, ContainerLogTarget, int64) (io.ReadCloser, error)
}

// LogSelection is the resolved log target of one request. Kind distinguishes
// the training plane from the orchestration plane so the response cannot claim
// a worker container for a KFP step.
type LogSelection struct {
	Kind   LogSourceKind
	Target ContainerLogTarget
	LogID  string
}

type LogSourceKind string

const (
	LogSourceKindTrainer LogSourceKind = "TRAINER"
	LogSourceKindKFPStep LogSourceKind = "KFP_STEP"
)

// DefaultContainerFor names the container a source reads when the request does
// not select one: training workers expose `node`, KFP steps expose `main`.
func DefaultContainerFor(kind LogSourceKind) string {
	if kind == LogSourceKindKFPStep {
		return "main"
	}
	return "node"
}
