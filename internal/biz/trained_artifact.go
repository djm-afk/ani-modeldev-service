package biz

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// TrainedArtifactRequest always comes from the persisted original delivery.
// Neither a callback nor a recovery request may select another target or key.
type TrainedArtifactRequest struct {
	TenantID, ExecutionID string
	Registration          FinetuningRegistration
}

// TrainedArtifactSession is the durable provider association. A pending upload
// session is not a model container or a READY model version.
type TrainedArtifactSession struct {
	RegistrationID string
	StoragePath    string
}

// TrainedArtifactUploadGrant exists only during one controlled copy. Signed
// URLs and headers must never be serialized into execution facts or errors.
type TrainedArtifactUploadGrant struct {
	Session   TrainedArtifactSession
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

type TrainedArtifactObservation struct {
	Session        TrainedArtifactSession
	State          string
	ObjectVerified bool
	Receipt        *FinetuningModelReceipt
}

// Each method makes one provider request. The durable execution aggregate,
// rather than this outbound port, arbitrates every write and communication
// recovery. Original-key observation never creates or completes a session.
type TrainedArtifactRegistry interface {
	PrepareArtifact(context.Context, TrainedArtifactRequest) (TrainedArtifactSession, error)
	ReadArtifact(context.Context, TrainedArtifactRequest) (TrainedArtifactObservation, error)
	ArtifactUploadGrant(context.Context, TrainedArtifactRequest, TrainedArtifactSession) (TrainedArtifactUploadGrant, error)
	CompleteArtifact(context.Context, TrainedArtifactRequest, TrainedArtifactSession) (FinetuningModelReceipt, error)
}

type TrainedArtifactCopier interface {
	CopyTrainedArtifact(context.Context, Execution, cpup01.FixedObjectRef, TrainedArtifactUploadGrant) error
}
