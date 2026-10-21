package component

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	workspaceio "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (runner *Runner) configuration(ctx context.Context) (biz.Execution, *trainingv1.WorkspaceRef, error) {
	call, err := runner.callContext(ctx)
	if err != nil {
		return biz.Execution{}, nil, err
	}
	response, err := runner.client.GetExecutionConfiguration(call, &modeldevv1.GetExecutionConfigurationRequest{Context: runner.config.Context})
	if err != nil {
		return biz.Execution{}, nil, err
	}
	admission := response.GetAdmission()
	if admission == nil || admission.Identity == nil || admission.AcceptedAt == nil || admission.AcceptedAt.CheckValid() != nil || admission.ResourceTenantId != runner.config.TenantID || !proto.Equal(response.Identity, admission.Identity) || !proto.Equal(response.Snapshot, admission.Snapshot) {
		return biz.Execution{}, nil, ErrConfiguration
	}
	expected := proto.Clone(runner.config.Context.Identity).(*trainingv1.ExecutionIdentity)
	if expected.OperationId == "" {
		expected.OperationId = admission.Identity.OperationId
	}
	if !proto.Equal(admission.Identity, expected) {
		return biz.Execution{}, nil, ErrConfiguration
	}
	authority, association := response.GetAuthority(), runner.config.Context.Association
	if authority == nil || authority.KfpRunId != association.KfpRunId || authority.NamespaceUid != association.NamespaceUid || authority.WorkflowUid != association.WorkflowUid {
		return biz.Execution{}, nil, ErrConfiguration
	}
	snapshot, err := contractpb.DecodeSnapshot(admission.Snapshot)
	if err != nil {
		return biz.Execution{}, nil, ErrConfiguration
	}
	intent, err := contractpb.DecodeIntent(admission.Intent)
	if err != nil {
		return biz.Execution{}, nil, ErrConfiguration
	}
	execution := biz.Execution{Admission: biz.Admission{TenantID: admission.ResourceTenantId, Actor: admission.AdmittedActorId, OperationID: admission.Identity.OperationId, ExecutionID: admission.Identity.ExecutionId, SpecHash: admission.Identity.ExecutionSpecHash, Intent: intent, IntentHash: admission.IntentHash, Snapshot: snapshot, AcceptedAt: admission.AcceptedAt.AsTime()}}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return biz.Execution{}, nil, ErrConfiguration
	}
	if snapshot.Environment.NamespaceName != association.NamespaceName || snapshot.Environment.NamespaceUID != association.NamespaceUid {
		return biz.Execution{}, nil, ErrConfiguration
	}
	if taskID := response.GetKfpTaskId(); taskID != "" {
		if len(taskID) > 256 || strings.IndexFunc(taskID, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0 || (runner.config.TaskID != "" && runner.config.TaskID != taskID) {
			return biz.Execution{}, nil, ErrConfiguration
		}
		runner.config.TaskID = taskID
	}
	// Only the authenticated, canonically checked committed admission can fill
	// the operation omitted by KFP's execution_id/spec_hash parameters.
	runner.config.Context.Identity = proto.Clone(admission.Identity).(*trainingv1.ExecutionIdentity)
	runner.executionKind = snapshot.Kind
	runner.stepTiming = nil
	if response.Timing != nil {
		runner.stepTiming = proto.Clone(response.Timing).(*modeldevv1.StepTiming)
	}
	return execution, response.Workspace, nil
}

func (runner *Runner) prepare(ctx context.Context) error {
	if runner.kube == nil || runner.store == nil || !absoluteClean(runner.config.WorkspaceDirectory) || runner.config.PVCName == "" {
		return ErrConfiguration
	}
	call, err := runner.callContext(ctx)
	if err != nil {
		return err
	}
	if _, err := runner.client.BeginExecution(call, &modeldevv1.BeginExecutionRequest{Context: runner.config.Context}); err != nil {
		return err
	}
	execution, _, err := runner.configuration(ctx)
	if err != nil {
		return err
	}

	environment := execution.Snapshot.Environment
	namespace, err := runner.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, environment.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return ErrConfiguration
	}
	if namespace.GetAPIVersion() != "v1" || namespace.GetKind() != "Namespace" || namespace.GetName() != environment.NamespaceName || string(namespace.GetUID()) != environment.NamespaceUID || namespace.GetDeletionTimestamp() != nil {
		return ErrConfiguration
	}
	pvc, err := runner.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(environment.NamespaceName).Get(ctx, runner.config.PVCName, metav1.GetOptions{})
	if err != nil || pvc == nil {
		return ErrConfiguration
	}
	if pvc.GetAPIVersion() != "v1" || pvc.GetKind() != "PersistentVolumeClaim" || pvc.GetNamespace() != environment.NamespaceName || pvc.GetName() != runner.config.PVCName || pvc.GetUID() == "" || pvc.GetDeletionTimestamp() != nil {
		return ErrConfiguration
	}
	want := execution.Snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: want.Mode, NamespaceName: environment.NamespaceName, NamespaceUID: environment.NamespaceUID, PVCName: runner.config.PVCName, PVCUID: string(pvc.GetUID()), InputSubpath: want.InputSubpath, TrainingSubpath: want.TrainingSubpath, ReportsSubpath: want.ReportsSubpath, PublicationSubpath: want.PublicationSubpath}
	if err := ensureWorkspaceDirectories(runner.config.WorkspaceDirectory, want.TrainingSubpath, want.PublicationSubpath); err != nil {
		return err
	}
	var prepared biz.WorkspaceBinding
	if execution.Snapshot.Kind == "FINETUNING" {
		prepared, err = workspaceio.PrepareFinetuningInput(ctx, runner.store, runner.baseStore, execution, binding, runner.config.WorkspaceDirectory)
	} else {
		prepared, err = workspaceio.PrepareInput(ctx, runner.store, execution, binding, runner.config.WorkspaceDirectory)
	}
	if err != nil {
		return err
	}
	snapshot, err := contractpb.EncodeSnapshot(execution.Snapshot)
	if err != nil {
		return ErrConfiguration
	}
	call, err = runner.callContext(ctx)
	if err != nil {
		return err
	}
	_, err = runner.client.ReportStepResult(call, &modeldevv1.ReportStepResultRequest{Context: runner.config.Context, ReportId: runner.reportID("prepared"), Result: &modeldevv1.ReportStepResultRequest_Prepared{Prepared: &modeldevv1.PreparedInputCandidate{Input: snapshot.Input, Workspace: workspaceWire(prepared), PreparedManifest: &trainingv1.ManifestRef{RelativePath: "prepared-manifest.json", SizeBytes: prepared.PreparedManifestBytes, Sha256: prepared.PreparedManifestSHA256}}}})
	return err
}

func (runner *Runner) collect(ctx context.Context) error {
	execution, binding, err := runner.configuration(ctx)
	if err != nil {
		return err
	}
	if err := runner.checkWorkspace(execution, binding); err != nil {
		return err
	}
	if !runner.inventoryPath(execution) {
		return ErrConfiguration
	}
	directory := filepath.Join(runner.config.WorkspaceDirectory, binding.TrainingSubpath)
	if execution.Snapshot.Kind == "FINETUNING" {
		directory = runner.config.WorkspaceDirectory
	}
	output, err := workspaceio.Collect(ctx, directory, execution)
	if err != nil {
		return err
	}
	return writeJSON(runner.config.InventoryFile, output)
}

func (runner *Runner) publish(ctx context.Context) error {
	if runner.store == nil || !absoluteClean(runner.config.CandidateFile) {
		return ErrConfiguration
	}
	execution, binding, err := runner.configuration(ctx)
	if err != nil {
		return err
	}
	if runner.config.TaskID == "" {
		return ErrConfiguration
	}
	if err := runner.checkWorkspace(execution, binding); err != nil {
		return err
	}
	if !runner.inventoryPath(execution) {
		return ErrConfiguration
	}
	var output biz.CollectedOutput
	if err := readJSON(runner.config.InventoryFile, &output); err != nil {
		return err
	}
	directory := filepath.Join(runner.config.WorkspaceDirectory, binding.TrainingSubpath)
	if execution.Snapshot.Kind == "FINETUNING" {
		directory = runner.config.WorkspaceDirectory
	}
	candidate, err := workspaceio.UploadOutput(ctx, runner.store, execution, directory, output)
	if err != nil {
		return err
	}
	a := runner.config.Context.Association
	candidate.Upload = biz.UploadCompletion{RunID: a.KfpRunId, WorkflowUID: a.WorkflowUid, TaskID: runner.config.TaskID, PodUID: a.PodUid}
	return writeJSON(runner.config.CandidateFile, candidate)
}

func (runner *Runner) inventoryPath(execution biz.Execution) bool {
	subpath := execution.Snapshot.Workspace.ReportsSubpath
	if execution.Snapshot.Kind == "FINETUNING" {
		subpath = execution.Snapshot.Workspace.PublicationSubpath
	}
	return reportPath(runner.config.InventoryFile, runner.config.WorkspaceDirectory, subpath)
}

func (runner *Runner) close(ctx context.Context) (firstError error) {
	defer func() {
		if firstError == nil {
			return
		}
		// A failed publication must still fence creation. Preserve the original
		// failure even if this bounded cleanup request also fails.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		call, err := runner.callContext(cleanup)
		if err != nil {
			return
		}
		_, _ = runner.client.RequestExecutionClose(call, &modeldevv1.RequestExecutionCloseRequest{Context: runner.config.Context, Reason: modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED})
	}()
	if _, _, err := runner.configuration(ctx); err != nil {
		return err
	}
	if runner.executionKind == "FINETUNING" {
		return runner.requestClose(ctx, modeldevv1.CloseReason_CLOSE_REASON_NATURAL_TERMINAL)
	}
	if err := runner.confirmPublication(ctx); err != nil {
		return err
	}
	return runner.requestClose(ctx, modeldevv1.CloseReason_CLOSE_REASON_NATURAL_TERMINAL)
}

func (runner *Runner) confirmPublication(ctx context.Context) error {
	if runner.config.Context.Identity.OperationId == "" || runner.stepTiming == nil {
		if _, _, err := runner.configuration(ctx); err != nil {
			return err
		}
	}
	if runner.stepTiming == nil || runner.stepTiming.DeadlineAt == nil || runner.stepTiming.DeadlineAt.CheckValid() != nil {
		return ErrConfiguration
	}
	business, cancel := context.WithDeadline(ctx, runner.stepTiming.DeadlineAt.AsTime())
	defer cancel()
	ctx = business
	if err := ctx.Err(); err != nil {
		return err
	}
	if !absoluteClean(runner.config.CandidateFile) {
		return ErrConfiguration
	}
	var candidate biz.RuntimePublication
	if err := readJSON(runner.config.CandidateFile, &candidate); err != nil {
		return err
	}
	wire, err := candidateWire(candidate)
	if err != nil {
		return err
	}
	for {
		call, err := runner.callContext(ctx)
		if err != nil {
			return err
		}
		_, err = runner.client.ReportStepResult(call, &modeldevv1.ReportStepResultRequest{Context: runner.config.Context, ReportId: runner.reportID("publication"), Result: &modeldevv1.ReportStepResultRequest_Publication{Publication: wire}})
		if err == nil {
			break
		}
		if !retryObservation(err) {
			return err
		}
		if err := runner.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) requestClose(ctx context.Context, reason modeldevv1.CloseReason) error {
	for {
		call, err := runner.callContext(ctx)
		if err != nil {
			return err
		}
		result, err := runner.client.RequestExecutionClose(call, &modeldevv1.RequestExecutionCloseRequest{Context: runner.config.Context, Reason: reason})
		if err != nil {
			if !retryObservation(err) {
				return err
			}
		} else {
			switch result.GetCloseState() {
			case modeldevv1.CloseState_CLOSE_STATE_CLOSED:
				return nil
			case modeldevv1.CloseState_CLOSE_STATE_NEEDS_REVIEW:
				return ErrNeedsReview
			case modeldevv1.CloseState_CLOSE_STATE_CLOSING:
				if reason == modeldevv1.CloseReason_CLOSE_REASON_NATURAL_TERMINAL || runner.config.CloseOnly {
					if result.GetCloseGeneration() == 0 || result.GetAcceptedAt() == nil || result.GetAcceptedAt().CheckValid() != nil || result.GetAcceptedAt().AsTime().IsZero() {
						return ErrConfiguration
					}
					// Completing this control task lets KFP/Argo finish naturally;
					// CLOSING remains pending until the owner's full writer proof.
					return nil
				}
			default:
				return ErrConfiguration
			}
		}
		if err := runner.wait(ctx); err != nil {
			return err
		}
	}
}

func (runner *Runner) checkWorkspace(execution biz.Execution, binding *trainingv1.WorkspaceRef) error {
	if binding == nil || !absoluteClean(runner.config.WorkspaceDirectory) {
		return ErrConfiguration
	}
	s := execution.Snapshot
	if binding.Mode != trainingv1.WorkspaceMode_WORKSPACE_MODE_EXECUTION_PVC || binding.NamespaceName != s.Environment.NamespaceName || binding.NamespaceUid != s.Environment.NamespaceUID || binding.PvcUid == "" || binding.PvcName == "" || binding.InputSubpath != s.Workspace.InputSubpath || binding.TrainingSubpath != s.Workspace.TrainingSubpath || binding.ReportsSubpath != s.Workspace.ReportsSubpath || binding.PublicationSubpath != s.Workspace.PublicationSubpath {
		return ErrConfiguration
	}
	return nil
}

func workspaceWire(b biz.WorkspaceBinding) *trainingv1.WorkspaceRef {
	return &trainingv1.WorkspaceRef{Mode: trainingv1.WorkspaceMode(trainingv1.WorkspaceMode_value["WORKSPACE_MODE_"+b.Mode]), NamespaceName: b.NamespaceName, NamespaceUid: b.NamespaceUID, PvcName: b.PVCName, PvcUid: b.PVCUID, InputSubpath: b.InputSubpath, TrainingSubpath: b.TrainingSubpath, ReportsSubpath: b.ReportsSubpath, PublicationSubpath: b.PublicationSubpath}
}

func candidateWire(candidate biz.RuntimePublication) (*modeldevv1.PublicationCandidate, error) {
	if candidate.LogicalKey == "" || candidate.Upload.TaskID == "" || candidate.Upload.PodUID == "" || len(candidate.Files) == 0 {
		return nil, ErrConfiguration
	}
	manifest, err := objectWire(candidate.Manifest)
	if err != nil {
		return nil, err
	}
	result := &modeldevv1.PublicationCandidate{LogicalPublicationKey: candidate.LogicalKey, ManifestObject: manifest, UploadTaskId: candidate.Upload.TaskID, UploadPodUid: candidate.Upload.PodUID}
	if candidate.Bundle != nil {
		result.BundleObject, err = objectWire(*candidate.Bundle)
		if err != nil {
			return nil, err
		}
	}
	for _, file := range candidate.Files {
		object, err := objectWire(file.Object)
		if err != nil {
			return nil, err
		}
		role, ok := trainingv1.FileRole_value["FILE_ROLE_"+file.File.Role]
		if !ok || role == 0 {
			return nil, ErrConfiguration
		}
		result.Files = append(result.Files, &modeldevv1.PublishedFileCandidate{File: &trainingv1.FileEntry{RelativePath: file.File.RelativePath, Role: trainingv1.FileRole(role), SizeBytes: file.File.SizeBytes, Sha256: file.File.SHA256}, Object: object})
	}
	return result, nil
}

func objectWire(object cpup01.FixedObjectRef) (*trainingv1.FixedObjectRef, error) {
	if object.VersionID == nil || object.ImmutableCopy != nil {
		return nil, ErrConfiguration
	}
	return &trainingv1.FixedObjectRef{StorageConnectionId: object.StorageConnectionID, Bucket: object.Bucket, Key: object.Key, Immutability: &trainingv1.FixedObjectRef_VersionId{VersionId: *object.VersionID}, SizeBytes: object.SizeBytes, Sha256: object.SHA256}, nil
}

func absoluteClean(name string) bool {
	return filepath.IsAbs(name) && filepath.Clean(name) == name && name != "/"
}

func ensureWorkspaceDirectories(root string, subpaths ...string) error {
	base, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrConfiguration
	}
	defer unix.Close(base)
	for _, subpath := range subpaths {
		current, err := unix.Dup(base)
		if err != nil {
			return ErrConfiguration
		}
		for _, name := range strings.Split(subpath, "/") {
			if name == "" || name == "." || name == ".." {
				unix.Close(current)
				return ErrConfiguration
			}
			if err := unix.Mkdirat(current, name, 0700); err != nil && err != unix.EEXIST {
				unix.Close(current)
				return ErrConfiguration
			}
			next, err := unix.Openat(current, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(current)
			if err != nil {
				return ErrConfiguration
			}
			current = next
		}
		unix.Close(current)
	}
	return nil
}

func reportPath(file, root, subpath string) bool {
	if !absoluteClean(file) {
		return false
	}
	directory := filepath.Join(root, subpath)
	relative, err := filepath.Rel(directory, file)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readToken(name string) ([]byte, error) {
	// Kubernetes projected tokens use an atomically rotated symlink. This path
	// belongs to owner configuration, never to a tenant-supplied RPC body.
	file, err := os.Open(name)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer file.Close()
	return readLimited(file, 16384)
}

func readRegular(name string, limit int64) ([]byte, error) {
	if !absoluteClean(name) {
		return nil, ErrConfiguration
	}
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConfiguration
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	return readLimited(file, limit)
}

func readLimited(file *os.File, limit int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, ErrConfiguration
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrConfiguration
	}
	return data, nil
}

func readJSON(name string, target any) error {
	data, err := readRegular(name, 4<<20)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrConfiguration
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrConfiguration
	}
	return nil
}

func writeJSON(name string, value any) error {
	if !absoluteClean(name) {
		return ErrConfiguration
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > 4<<20 {
		return ErrConfiguration
	}
	directory := filepath.Dir(name)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return ErrConfiguration
	}
	file, err := os.CreateTemp(directory, ".managed-output-")
	if err != nil {
		return ErrConfiguration
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return ErrConfiguration
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return ErrConfiguration
	}
	if err := file.Close(); err != nil {
		return ErrConfiguration
	}
	if err := os.Rename(temporary, name); err != nil {
		return ErrConfiguration
	}
	return nil
}
