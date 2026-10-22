package biz

import (
	"context"
	"strings"
	"time"
)

// TemporaryStorageCredentials belong to one persisted execution and purpose.
// They are returned only to its currently verified prepare or publish Pod.
type TemporaryStorageCredentials struct {
	AccessKeyID         string
	SecretAccessKey     string
	SessionToken        string
	ExpiresAt           time.Time
	StorageConnectionID string
	Bucket              string
	ObjectKey           string
	ObjectPrefix        string
}

type StorageCredentialIssuer interface {
	IssueForExecution(context.Context, Execution, string) (TemporaryStorageCredentials, error)
}

type ManagedStorageCredentialsResult struct {
	Runtime     ManagedRuntimeResult
	Credentials TemporaryStorageCredentials
}

// StorageCredentials rechecks workload and Run authority on every issuance or
// refresh. An old credential, receipt or prior successful callback grants none.
func (runtime *ManagedRuntime) StorageCredentials(ctx context.Context, token string, request BeginManagedExecutionRequest, purpose string, issuer StorageCredentialIssuer) (ManagedStorageCredentialsResult, error) {
	if purpose != "prepare" && purpose != "prepare-base-model" && purpose != "publish" {
		return ManagedStorageCredentialsResult{}, ErrManagedStepUnauthorized
	}
	task := purpose
	if purpose == "prepare-base-model" {
		task = "prepare"
	}
	result, err := runtime.Configuration(ctx, token, request, task)
	if err != nil {
		return ManagedStorageCredentialsResult{}, err
	}
	if issuer == nil || !storageCredentialsAllowed(result, purpose) {
		return ManagedStorageCredentialsResult{}, ErrRuntimeNotReady
	}
	credentials, err := issuer.IssueForExecution(ctx, result.Execution, purpose)
	if err != nil {
		if ctx.Err() != nil {
			return ManagedStorageCredentialsResult{}, ctx.Err()
		}
		return ManagedStorageCredentialsResult{}, ErrManagedStepUnavailable
	}
	if !validTemporaryCredentials(result.Execution, purpose, credentials) {
		return ManagedStorageCredentialsResult{}, ErrManagedStepUnavailable
	}
	// STS is an external call. A close committed during that call must prevent
	// handing its secret to a writer even when the remote session was created.
	state, err := runtime.repository.GetRuntime(ctx, result.Execution.TenantID, result.Execution.ExecutionID)
	if err != nil {
		return ManagedStorageCredentialsResult{}, err
	}
	current, err := runtimeResult(result, state, false)
	if err != nil {
		return ManagedStorageCredentialsResult{}, err
	}
	if !storageCredentialsAllowed(current, purpose) {
		return ManagedStorageCredentialsResult{}, ErrRuntimeNotReady
	}
	return ManagedStorageCredentialsResult{Runtime: current, Credentials: credentials}, nil
}

func storageCredentialsAllowed(result ManagedRuntimeResult, purpose string) bool {
	if result.States.Close != CloseStateOpen || result.Runtime.CloseGeneration != 0 || result.Runtime.ClosedAt != nil ||
		!result.Execution.Snapshot.DeadlineAt.After(time.Now()) || result.States.Compute == ComputeStateFailed ||
		result.States.Delivery == DeliveryStatePublished || result.Runtime.Publication != nil {
		return false
	}
	if purpose == "prepare" || purpose == "prepare-base-model" {
		return result.Runtime.Training == nil
	}
	return result.Runtime.Observation != nil && result.Runtime.Observation.Outcome == "SUCCEEDED" && result.Runtime.Observation.WritersAbsent
}

func validTemporaryCredentials(execution Execution, purpose string, credentials TemporaryStorageCredentials) bool {
	for _, value := range []string{credentials.AccessKeyID, credentials.SecretAccessKey, credentials.SessionToken} {
		if value == "" || len(value) > 16384 || strings.ContainsAny(value, " \t\r\n") {
			return false
		}
	}
	if !credentials.ExpiresAt.After(time.Now().Add(2*time.Minute)) || credentials.ExpiresAt.After(time.Now().Add(16*time.Minute)) {
		return false
	}
	if purpose == "prepare" {
		input := execution.Snapshot.Input.Object
		return credentials.StorageConnectionID == input.StorageConnectionID && credentials.Bucket == input.Bucket && credentials.ObjectKey == input.Key && credentials.ObjectPrefix == ""
	}
	if purpose == "prepare-base-model" {
		fine := execution.Snapshot.Finetuning
		if execution.Snapshot.Kind != "FINETUNING" || fine == nil {
			return false
		}
		object := fine.BaseModel.Object
		return credentials.StorageConnectionID == object.StorageConnectionID && credentials.Bucket == object.Bucket && credentials.ObjectKey == object.Key && credentials.ObjectPrefix == ""
	}
	scope := execution.Snapshot.PublicationScope
	return credentials.StorageConnectionID == scope.StorageConnectionID && credentials.Bucket == scope.Bucket && credentials.ObjectKey == "" && credentials.ObjectPrefix == scope.ApprovedPrefix+"/"+execution.ExecutionID+"/"
}
