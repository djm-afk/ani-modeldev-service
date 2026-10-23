// Package runtimeproof verifies managed step claims through current external
// APIs. Its observations must still commit through the execution repository.
package runtimeproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	workspaceio "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type PlanReader interface {
	Get(context.Context, string, string) (biz.PipelineDispatch, error)
}
type Verifier struct {
	kube     dynamic.Interface
	runs     *kfp.Client
	objects  *objectstore.Verifier
	plans    PlanReader
	logs     biz.TrainingLogReader
	training biz.TrainingRuntime
}

func New(kube dynamic.Interface, runs *kfp.Client, objects *objectstore.Verifier, plans PlanReader) *Verifier {
	return &Verifier{kube: kube, runs: runs, objects: objects, plans: plans, training: trainer.New(kube)}
}

// WithTrainingRuntime preserves the runtime's configured owner capabilities
// when cleanup independently refreshes the original terminal observation.
func (verifier *Verifier) WithTrainingRuntime(training biz.TrainingRuntime) *Verifier {
	verifier.training = training
	return verifier
}

func (verifier *Verifier) VerifyPrepared(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, workspace biz.WorkspaceBinding) error {
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return err
	}
	task, err := oneTask(tasks, "prepare")
	if err != nil || task.PodName != association.PodName {
		return biz.ErrRuntimeConflict
	}
	pod, err := verifier.currentTaskPod(ctx, execution, association, task, association.PodUID)
	if err != nil {
		return err
	}
	if err := biz.ValidateWorkspaceBinding(execution, workspace); err != nil {
		return err
	}
	manifest, err := workspaceio.PreparedManifestBytes(execution, workspace)
	if err != nil || int64(len(manifest)) != workspace.PreparedManifestBytes || contentSHA(manifest) != workspace.PreparedManifestSHA256 {
		return biz.ErrRuntimeConflict
	}
	pvc, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(workspace.NamespaceName).Get(ctx, workspace.PVCName, metav1.GetOptions{})
	if err != nil {
		return biz.ErrRuntimeNotReady
	}
	if !sameObject(pvc, "v1", "PersistentVolumeClaim", workspace.NamespaceName, workspace.PVCName, workspace.PVCUID) || pvc.GetDeletionTimestamp() != nil {
		return biz.ErrRuntimeConflict
	}
	phase, _ := textAt(pvc, "status", "phase")
	storageClass, _ := textAt(pvc, "spec", "storageClassName")
	storage, _ := textAt(pvc, "spec", "resources", "requests", "storage")
	quantity, err := resource.ParseQuantity(storage)
	if err != nil || quantity.Cmp(*resource.NewQuantity(execution.Snapshot.Workspace.CapacityBytes, resource.BinarySI)) != 0 || phase != "Bound" || storageClass != execution.Snapshot.Workspace.StorageClass || !preparedMount(pod, workspace.PVCName) {
		return biz.ErrRuntimeConflict
	}
	return nil
}
func (verifier *Verifier) VerifyPublication(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation, candidate biz.RuntimePublication) (biz.RuntimePublication, error) {
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return biz.RuntimePublication{}, err
	}
	confirmationRole := "close"
	if execution.Snapshot.Kind == "FINETUNING" {
		confirmationRole = "register"
	}
	confirmationTask, err := oneTask(tasks, confirmationRole)
	if err != nil || confirmationTask.PodName != association.PodName {
		return biz.RuntimePublication{}, biz.ErrRuntimeConflict
	}
	uploader, err := oneTask(tasks, "publish")
	if err != nil || uploader.ID != candidate.Upload.TaskID || uploader.State != "SUCCEEDED" || candidate.Upload.PodUID == "" {
		return biz.RuntimePublication{}, biz.ErrRuntimeNotReady
	}
	pod, err := verifier.currentTaskPod(ctx, execution, association, uploader, candidate.Upload.PodUID)
	if err != nil {
		return biz.RuntimePublication{}, err
	}
	terminal, zero, completedAt, mainCode := terminatedPod(pod)
	if !terminal || !zero || mainCode == nil || *mainCode != 0 {
		return biz.RuntimePublication{}, biz.ErrRuntimeNotReady
	}
	observedAt := time.Now().UTC()
	if completedAt.After(observedAt) {
		return biz.RuntimePublication{}, biz.ErrRuntimeNotReady
	}
	if verifier.objects == nil || candidate.LogicalKey != "cpu-p01:"+execution.ExecutionID {
		return biz.RuntimePublication{}, biz.ErrRuntimeConflict
	}
	files := make([]cpup01.OutputFile, len(candidate.Files))
	for i, file := range candidate.Files {
		files[i] = file.File
	}
	manifest, manifestSHA, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(execution.Admission), files)
	if err != nil || candidate.Manifest.SizeBytes != int64(len(manifest)) || candidate.Manifest.SHA256 != manifestSHA {
		return biz.RuntimePublication{}, biz.ErrRuntimeConflict
	}
	scope := execution.Snapshot.PublicationScope
	prefix := strings.TrimSuffix(scope.ApprovedPrefix, "/") + "/" + execution.ExecutionID + "/"
	if candidate.Manifest.Key != prefix+"output-manifest.json" {
		return biz.RuntimePublication{}, biz.ErrRuntimeConflict
	}
	publication := biz.RuntimePublication{LogicalKey: candidate.LogicalKey}
	identity := execution.TenantID + "\x00" + execution.ExecutionID + "\x00" + execution.SpecHash + "\x00" + candidate.LogicalKey
	publication.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("publication\x00"+identity)).String()
	publication.ReceiptID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("publication-receipt\x00"+identity)).String()
	for _, file := range candidate.Files {
		if file.Object.Key != prefix+file.File.RelativePath || file.Object.SizeBytes != file.File.SizeBytes || file.Object.SHA256 != file.File.SHA256 {
			return biz.RuntimePublication{}, biz.ErrRuntimeConflict
		}
		verified, err := verifier.objects.Verify(ctx, scope, file.Object)
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		file.Object = verified.Object
		file.ArtifactID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("artifact\x00"+identity+"\x00"+file.File.RelativePath)).String()
		publication.Files = append(publication.Files, file)
	}
	sort.Slice(publication.Files, func(i, j int) bool {
		return publication.Files[i].File.RelativePath < publication.Files[j].File.RelativePath
	})
	verifiedManifest, err := verifier.objects.Verify(ctx, scope, candidate.Manifest)
	if err != nil {
		return biz.RuntimePublication{}, err
	}
	publication.Manifest = verifiedManifest.Object
	if execution.Snapshot.OutputContract.CreateTarBundle != (candidate.Bundle != nil) {
		return biz.RuntimePublication{}, biz.ErrRuntimeConflict
	}
	if candidate.Bundle != nil {
		if candidate.Bundle.Key != prefix+"bundle.tar" {
			return biz.RuntimePublication{}, biz.ErrRuntimeConflict
		}
		var verified biz.VerifiedObject
		if execution.Snapshot.Kind == "FINETUNING" {
			verified, err = verifier.objects.VerifyFinetuningOutputBundle(ctx, scope, *candidate.Bundle, files, execution.Snapshot.OutputContract)
		} else {
			verified, err = verifier.objects.VerifyOutputBundle(ctx, scope, *candidate.Bundle, files, manifest)
		}
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		publication.Bundle = &verified.Object
	}
	publication.Upload = biz.UploadCompletion{RunID: association.RunID, WorkflowUID: association.WorkflowUID, TaskID: uploader.ID, PodUID: string(pod.GetUID()), ContainerName: "main", CompletedAt: completedAt, ObservedAt: observedAt}
	publication.VerifiedAt = time.Now().UTC()
	return publication, nil
}

func (verifier *Verifier) VerifyWritersAbsent(ctx context.Context, execution biz.Execution, association biz.ManagedStepAssociation) (biz.ManagedCloseEvidence, error) {
	tasks, err := verifier.currentTasks(ctx, execution, association)
	if err != nil {
		return biz.ManagedCloseEvidence{}, err
	}
	closeTask, err := closeTaskForPod(tasks, association.PodName)
	if err != nil || closeTask.PodName != association.PodName {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
	}
	closePod, err := verifier.currentTaskPod(ctx, execution, association, closeTask, association.PodUID)
	if err != nil || !controlOnly(closePod) {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	evidence := biz.ManagedCloseEvidence{RunID: association.RunID, WorkflowUID: association.WorkflowUID}
	seen := make(map[string]bool)
	roles := []string{"prepare", "train-wait", "collect", "publish"}
	if execution.Snapshot.Kind == "FINETUNING" {
		roles = []string{"prepare", "train-wait", "export", "verify", "collect", "publish", "register"}
	}
	for _, name := range roles {
		task, err := oneTask(tasks, name)
		if err != nil {
			return biz.ManagedCloseEvidence{}, err
		}
		if task.State == "SKIPPED" {
			if task.RunID != association.RunID || task.PodName != "" {
				return biz.ManagedCloseEvidence{}, biz.ErrRuntimeConflict
			}
			evidence.SkippedTasks = append(evidence.SkippedTasks, biz.ManagedSkippedTask{TaskName: name, TaskID: task.ID})
			continue
		}
		if !terminalTask(task.State) {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		pod, err := verifier.currentTaskPod(ctx, execution, association, task, "")
		if err != nil {
			return biz.ManagedCloseEvidence{}, err
		}
		terminal, _, _, code := terminatedPod(pod)
		if !terminal || code == nil || seen[string(pod.GetUID())] {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		seen[string(pod.GetUID())] = true
		evidence.Resources = append(evidence.Resources, podFact(pod, association.WorkflowUID, *code))
	}
	// Retried or older Workflow Pods may have fallen out of KFP's latest task
	// details. Enumerate the namespace and follow actual controller UIDs so such
	// writers cannot disappear merely through a changed label or task record.
	pods, err := verifier.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(association.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || pods.GetContinue() != "" {
		return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !workflowOwner(pod, association) || seen[string(pod.GetUID())] {
			continue
		}
		if pod.GetName() == association.PodName && string(pod.GetUID()) == association.PodUID {
			if !controlOnly(pod) {
				return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
			}
			continue
		}
		terminal, _, _, code := terminatedPod(pod)
		if !terminal || code == nil || pod.GetNamespace() != association.NamespaceName || pod.GetUID() == "" {
			return biz.ManagedCloseEvidence{}, biz.ErrRuntimeNotReady
		}
		seen[string(pod.GetUID())] = true
		evidence.Resources = append(evidence.Resources, podFact(pod, association.WorkflowUID, *code))
	}
	sort.Slice(evidence.Resources, func(i, j int) bool { return evidence.Resources[i].UID < evidence.Resources[j].UID })
	evidence.ObservedAt = time.Now().UTC()
	return evidence, nil
}

func contentSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
