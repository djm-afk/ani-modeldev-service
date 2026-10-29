package lifecycle_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

// TM07 replaces only external S3 and Kubernetes observations. UploadOutput,
// exact-version object verification, role timing, publication admission, Stop,
// and terminal persistence all use production implementations and real PG.
// The collected payload and terminal Pod/controller facts are explicit boundary
// fixtures; this regression does not claim to train a model or run live S3.
func TestPublicationWindowExpiryRejectsLateConfirmationButStillCloses(t *testing.T) {
	for _, mode := range []string{"before-expiry", "USER_STOP", "DEADLINE"} {
		t.Run(mode, func(t *testing.T) {
			open, admission, authority, binding := preparedRuntimeV21(t, 3)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := open()
			r := lifecycle.New(pool)
			reservation, err := reserveTrainingV21(t, r, ctx, authority)
			if err != nil || !reservation.SendPermit {
				t.Fatal("original training boundary", err)
			}
			handle := biz.TrainingHandle{NamespaceUID: binding.NamespaceUID, PVCUID: binding.PVCUID, TrainJobUID: "tm07-training-uid"}
			if _, err := r.RecordTrainingHandle(ctx, authority, handle); err != nil {
				t.Fatal(err)
			}
			zero := int32(0)
			observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{
				{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: binding.NamespaceName, Name: reservation.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
				{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: binding.NamespaceName, Name: "tm07-jobset", UID: "tm07-jobset-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
				{APIVersion: "v1", Kind: "Pod", Namespace: binding.NamespaceName, Name: "tm07-training", UID: "tm07-training-pod-uid", OwnerUID: "tm07-jobset-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero},
			}}
			if _, err := r.RecordTrainingObservation(ctx, authority, observation); err != nil {
				t.Fatal(err)
			}
			startManagedRoleV21(t, r, authority, "collect")
			startManagedRoleV21(t, r, authority, "publish")
			state, err := r.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			publishTiming := *biz.TimingForManagedRole(state, "publish")
			if !publishTiming.DeadlineAt.Equal(publishTiming.AdmittedAt.Add(3*time.Second)) || !publishTiming.DeadlineAt.Before(admission.Snapshot.DeadlineAt) {
				t.Fatal("PUBLISH must retain its own short window within the original total deadline")
			}
			exec, err := execution.New(open()).Get(ctx, admission.TenantID, admission.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			publication, verifyRemote := uploadTimingBoundary(t, ctx, exec, authority, publishTiming)
			verifyRemote(ctx)
			if mode == "before-expiry" {
				published, replay, err := r.RecordPublication(ctx, authority, publication)
				if err != nil || replay || published.Publication == nil {
					t.Fatal("on-time positive control did not commit publication", err)
				}
				if got, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID); err != nil || got.Publication == nil || got.Publication.ID != publication.ID {
					t.Fatal("on-time publication did not survive reconnect", err)
				}
				return
			}

			// Synchronize on the actual persisted DB deadline, not a guessed sleep
			// or an UPDATE of business timestamps/terminal rows.
			waitPublicationDBDeadline(t, ctx, pool, publishTiming.DeadlineAt)
			closeContext, closeCancel := context.WithTimeout(ctx, 5*time.Second)
			defer closeCancel()
			if closeContext.Err() != nil {
				t.Fatal("independent close context must still be available")
			}
			// Supplying this still-live cleanup budget cannot extend PUBLISH.
			if _, _, err := lifecycle.New(open()).RecordPublication(closeContext, authority, publication); !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
				t.Fatal("late first business confirmation borrowed close budget", err)
			}
			unchanged, err := lifecycle.New(open()).GetRuntime(closeContext, authority.TenantID, authority.ExecutionID)
			if err != nil || unchanged.Publication != nil || *biz.TimingForManagedRole(unchanged, "publish") != publishTiming {
				t.Fatal("late rejection wrote PUBLISHED or renewed the original window", err)
			}
			verifyRemote(closeContext)
			var closing biz.ExecutionRuntime
			if mode == "USER_STOP" {
				_, err = execution.New(open()).ApplyCloseIntent(closeContext, biz.CloseIntent{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: admission.Actor})
				if err == nil {
					closing, err = lifecycle.New(open()).GetRuntime(closeContext, authority.TenantID, authority.ExecutionID)
				}
			} else {
				closing, _, err = lifecycle.New(open()).RequestRuntimeClose(closeContext, authority, "DEADLINE")
			}
			if err != nil || closing.CloseGeneration == 0 || closing.CloseReason != mode || closing.ClosedAt != nil {
				t.Fatal("expired business window prevented the original close fence", err)
			}
			evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
			for _, timing := range closing.StepTimings {
				evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: timing.Binding.PodName, UID: timing.Binding.PodUID, OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
			}
			evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "tm07-train-wait", UID: "tm07-train-wait-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
			closed, err := lifecycle.New(open()).ConfirmRuntimeClosed(closeContext, authority, closing.CloseGeneration, observation, evidence)
			if err != nil || closed.ClosedAt == nil || closed.Publication != nil || closed.CloseReason != mode {
				t.Fatal("independent cleanup budget failed to persist proven close", err)
			}
			visible, err := lifecycle.New(open()).GetRuntime(closeContext, authority.TenantID, authority.ExecutionID)
			if err != nil || visible.ClosedAt == nil || visible.CloseGeneration != closing.CloseGeneration || visible.Publication != nil || *biz.TimingForManagedRole(visible, "publish") != publishTiming {
				t.Fatal("reconnect lost closure or turned retained bytes into publication", err)
			}
			verifyRemote(closeContext)
			t.Log("TM07 FEDORA: real uploader/remote bytes retained; expired first PUBLISH confirmation rejected; independent", mode, "close committed through restricted PG")
		})
	}
}

func waitPublicationDBDeadline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deadline time.Time) {
	t.Helper()
	for {
		var databaseNow time.Time
		if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
			t.Fatal(err)
		}
		if !databaseNow.Before(deadline) {
			return
		}
		timer := time.NewTimer(deadline.Sub(databaseNow))
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
}

func uploadTimingBoundary(t *testing.T, ctx context.Context, exec biz.Execution, authority biz.RunAuthorityCandidate, timing biz.ManagedStepTiming) (biz.RuntimePublication, func(context.Context)) {
	t.Helper()
	var mu sync.Mutex
	objects := make(map[string][]byte)
	const version = "tm07-original-version"
	prefix := "/" + exec.Snapshot.PublicationScope.Bucket + "/" + strings.TrimSuffix(exec.Snapshot.PublicationScope.ApprovedPrefix, "/") + "/" + exec.ExecutionID + "/"
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(request.URL.Path, prefix) {
			t.Error("S3 boundary escaped original execution scope")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch request.Method {
		case http.MethodPut:
			if request.Header.Get("If-None-Match") != "*" || objects[request.URL.Path] != nil {
				t.Error("production uploader lost create-only semantics")
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			content, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[request.URL.Path] = content
			w.Header().Set("x-amz-version-id", version)
		case http.MethodGet:
			content, exists := objects[request.URL.Path]
			if !exists || request.URL.Query().Get("versionId") != version {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("x-amz-version-id", version)
			_, _ = w.Write(content)
		default:
			t.Error("unexpected remote mutation or request", request.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(peer.Close)
	client := s3.New(s3.Options{Region: "tm07-test", BaseEndpoint: aws.String(peer.URL), UsePathStyle: true, HTTPClient: peer.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired})
	directory := t.TempDir()
	output := biz.CollectedOutput{ExecutionID: exec.ExecutionID, ExecutionSpecHash: exec.SpecHash, StorageState: "WORKSPACE_ONLY"}
	for _, required := range exec.Snapshot.OutputContract.RequiredFiles {
		// These already-collected bytes are an explicit input boundary to the
		// uploader, not invented successful metrics or a training result.
		content := []byte("TM07 collected-file boundary: " + required.RelativePath + "\n")
		path := filepath.Join(directory, required.RelativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		output.Files = append(output.Files, cpup01.OutputFile{RelativePath: required.RelativePath, Role: required.Role, InTar: required.InTar, SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])})
	}
	var err error
	output.Manifest, output.ManifestSHA256, err = cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(exec.Admission), output.Files)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := workspace.UploadOutput(ctx, client, exec, directory, output)
	if err != nil {
		t.Fatal("real CPU publication uploader", err)
	}
	verifier := objectstore.NewVerifier(client, exec.Snapshot.PublicationScope.StorageConnectionID)
	verify := func(ctx context.Context) {
		t.Helper()
		for _, file := range candidate.Files {
			if _, err := verifier.Verify(ctx, exec.Snapshot.PublicationScope, file.Object); err != nil {
				t.Fatal("exact original remote file no longer independently verifies", err)
			}
		}
		if _, err := verifier.Verify(ctx, exec.Snapshot.PublicationScope, candidate.Manifest); err != nil {
			t.Fatal("remote manifest", err)
		}
		if candidate.Bundle == nil {
			t.Fatal("CPU output bundle absent")
		}
		if _, err := verifier.VerifyOutputBundle(ctx, exec.Snapshot.PublicationScope, *candidate.Bundle, output.Files, output.Manifest); err != nil {
			t.Fatal("remote CPU bundle bytes or membership changed", err)
		}
	}
	verify(ctx)
	mu.Lock()
	originalObjects := make(map[string][]byte, len(objects))
	for name, content := range objects {
		originalObjects[name] = bytes.Clone(content)
	}
	mu.Unlock()
	verifyUnchanged := func(ctx context.Context) {
		verify(ctx)
		mu.Lock()
		defer mu.Unlock()
		if !reflect.DeepEqual(objects, originalObjects) {
			t.Fatal("expiry/close changed or deleted remote publication bytes")
		}
	}
	candidate.ID = uuid.NewString()
	candidate.ReceiptID = "tm07-verified-original-receipt"
	candidate.Upload = biz.UploadCompletion{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: timing.Binding.TaskID, PodUID: timing.Binding.PodUID, ContainerName: "main", CompletedAt: time.Now().UTC()}
	candidate.Upload.ObservedAt = candidate.Upload.CompletedAt
	candidate.VerifiedAt = candidate.Upload.ObservedAt
	for i := range candidate.Files {
		candidate.Files[i].ArtifactID = uuid.NewString()
	}
	return candidate, verifyUnchanged
}
