package dataset

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// ImporterWorkload is the namespace-side adapter for the importer Job. It owns
// only the objects a single attempt created: the Job, its PVC and its credential
// Secret. It never provisions a shared environment, pulls a training profile, or
// writes a RoleBinding.
//
// The fixed shape below is what the native admission policy pins: the importer
// ServiceAccount with automatic token mounting off, the dataset-import purpose
// label, suspend=true at create, restartPolicy=Never and backoffLimit=0. A Job
// that does not match that shape is denied by the API server, which is the
// intended second line of defence.
type ImporterWorkload struct {
	kube                                             dynamic.Interface
	clusterID                                        string
	importerImage                                    string
	importerDigest                                   string
	serviceAccount                                   string
	storageClass                                     string
	cpuRequest, cpuLimit, memoryRequest, memoryLimit string
	pvcSize                                          string
	planTimeout                                      time.Duration

	// Runtime endpoints the importer reads from its environment. They are the
	// platform's own values, never selected by a tenant or by a task.
	s3Endpoint     string
	s3Region       string
	huggingFaceURL string
	modelScopeURL  string
	credentialFile string
}

var (
	importerJobResource    = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	importerPVCResource    = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	importerSecretResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

// ImporterWorkloadConfig is the fixed material identity. It has no per-tenant
// fields: the tenant is carried by the attempt, not by this configuration.
type ImporterWorkloadConfig struct {
	ClusterID      string
	Image          string
	ImageDigest    string
	ServiceAccount string
	StorageClass   string
	CPURequest     string
	CPULimit       string
	MemoryRequest  string
	MemoryLimit    string
	PVCSize        string
	PlanTimeout    time.Duration

	S3Endpoint     string
	S3Region       string
	HuggingFaceURL string
	ModelScopeURL  string
	CredentialFile string
}

func NewImporterWorkload(config ImporterWorkloadConfig, kube dynamic.Interface) (*ImporterWorkload, error) {
	if kube == nil || !biz.CanonicalEnvironmentUUID(config.ClusterID) || config.Image == "" ||
		len(config.ImageDigest) != 64 || config.ServiceAccount == "" || config.StorageClass == "" ||
		config.CPURequest == "" || config.CPULimit == "" || config.MemoryRequest == "" ||
		config.MemoryLimit == "" || config.PVCSize == "" || config.PlanTimeout <= 0 ||
		config.S3Endpoint == "" || config.S3Region == "" || config.HuggingFaceURL == "" ||
		config.ModelScopeURL == "" || config.CredentialFile == "" {
		return nil, biz.ErrImportWorkerConfiguration
	}
	return &ImporterWorkload{
		kube: kube, clusterID: config.ClusterID, importerImage: config.Image, importerDigest: config.ImageDigest,
		serviceAccount: config.ServiceAccount, storageClass: config.StorageClass,
		cpuRequest: config.CPURequest, cpuLimit: config.CPULimit,
		memoryRequest: config.MemoryRequest, memoryLimit: config.MemoryLimit,
		pvcSize: config.PVCSize, planTimeout: config.PlanTimeout,
		s3Endpoint: config.S3Endpoint, s3Region: config.S3Region,
		huggingFaceURL: config.HuggingFaceURL, modelScopeURL: config.ModelScopeURL,
		credentialFile: config.CredentialFile,
	}, nil
}

// EnsureImporter creates the credential Secret, the PVC and the suspended Job
// when they are absent. An existing object with a different identity is a
// conflict, not an adoption: taking over a same-named object would let this
// attempt inherit another attempt's credentials.
func (w *ImporterWorkload) EnsureImporter(ctx context.Context, attempt biz.DatasetImportAttempt, plan biz.AttemptPlan, task biz.DatasetImportTask, credentials biz.TemporaryStorageCredentials) (biz.ImportWorkload, error) {
	if w == nil || w.kube == nil {
		return biz.ImportWorkload{}, biz.ErrImportWorkerConfiguration
	}
	if attempt.NamespaceName != "tenant-"+task.TenantID {
		return biz.ImportWorkload{}, biz.ErrImportEnvironmentMissing
	}
	if err := w.ensureSecret(ctx, attempt, task, plan, credentials); err != nil {
		return biz.ImportWorkload{}, err
	}
	if err := w.ensurePVC(ctx, attempt, task); err != nil {
		return biz.ImportWorkload{}, err
	}
	return w.ensureJob(ctx, attempt, plan, task)
}

// importerLabels satisfies the native policy's dataset identity: managed-by,
// tenant-id and the dataset-import purpose. A training registry row is optional:
// when present its environment id is recorded for audit, but the identity never
// depends on it (§7.3, §12.4).
func importerLabels(task biz.DatasetImportTask, attempt biz.DatasetImportAttempt) map[string]any {
	labels := map[string]any{
		"ani.io/managed-by":          "ani-modeldev-service",
		"modeldev.ani.io/purpose":    "dataset-import",
		"modeldev.ani.io/tenant-id":  task.TenantID,
		"modeldev.ani.io/dataset-id": task.DatasetID,
		"modeldev.ani.io/task-id":    task.TaskID,
		"modeldev.ani.io/attempt-id": attempt.AttemptID,
	}
	if task.EnvironmentID != "" {
		labels["modeldev.ani.io/environment-id"] = task.EnvironmentID
	}
	return labels
}

// importerAnnotations carries the Namespace UID and the frozen target digest the
// native policy expects as annotations. A 64-hex SHA-256 exceeds the 63-character
// label value limit, so the digest cannot be a label.
func importerAnnotations(task biz.DatasetImportTask, attempt biz.DatasetImportAttempt) map[string]any {
	return map[string]any{
		"modeldev.ani.io/namespace-uid":          attempt.NamespaceUID,
		"modeldev.ani.io/target-snapshot-sha256": task.TargetSnapshotSHA256,
	}
}

func (w *ImporterWorkload) ensureSecret(ctx context.Context, attempt biz.DatasetImportAttempt, task biz.DatasetImportTask, plan biz.AttemptPlan, credentials biz.TemporaryStorageCredentials) error {
	payload, err := json.Marshal(map[string]string{
		"access_key_id": credentials.AccessKeyID, "secret_access_key": credentials.SecretAccessKey,
		"session_token": credentials.SessionToken,
		// The expiry rides along so the importer can refuse to start an upload it
		// cannot finish, instead of letting the store answer AccessDenied halfway
		// through and reporting it as an opaque dependency failure.
		"expiration": credentials.ExpiresAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("%w: encode import credentials: %v", biz.ErrPersistence, err)
	}
	expected := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": attempt.SecretName, "namespace": attempt.NamespaceName,
			"labels": importerLabels(task, attempt), "annotations": importerAnnotations(task, attempt),
		},
		"type": "Opaque",
		// The frozen plan rides along in the attempt Secret: the control plane
		// already owns Secret create/get/patch/delete here, so no extra ConfigMap
		// verb is needed, and the pod still cannot be handed a plan that differs
		// from the digest frozen on the attempt row.
		"data": map[string]any{
			"credentials.json": base64Std(payload),
			"plan.json":        base64Std(plan.RuntimePlan),
		},
	}}
	_, err = w.ensureObject(ctx, importerSecretResource, expected, true)
	return err
}

func (w *ImporterWorkload) ensurePVC(ctx context.Context, attempt biz.DatasetImportAttempt, task biz.DatasetImportTask) error {
	storage, err := w.pvcStorage(task.ExpectedSizeBytes)
	if err != nil {
		return err
	}
	expected := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name": attempt.PVCName, "namespace": attempt.NamespaceName,
			"labels": importerLabels(task, attempt), "annotations": importerAnnotations(task, attempt),
		},
		"spec": map[string]any{
			"accessModes":      []any{"ReadWriteOnce"},
			"storageClassName": w.storageClass,
			"resources":        map[string]any{"requests": map[string]any{"storage": storage}},
		},
	}}
	_, err = w.ensureObject(ctx, importerPVCResource, expected, false)
	return err
}

// pvcStorage sizes the staging volume for the file this task will download.
//
// A fixed size silently capped every import at whatever the deployment happened
// to configure, so a file the platform accepted could never be staged. The
// request is the larger of the configured floor and the file plus a headroom
// factor, because the importer writes the download and then reads it back while
// uploading. Each attempt gets its own PVC, so this never expands an existing
// volume and needs no allowVolumeExpansion.
func (w *ImporterWorkload) pvcStorage(expectedSizeBytes int64) (string, error) {
	floor, err := resource.ParseQuantity(w.pvcSize)
	if err != nil || floor.Sign() <= 0 {
		return "", biz.ErrImportWorkerConfiguration
	}
	if expectedSizeBytes <= 0 {
		return w.pvcSize, nil
	}
	// ceil(S * 1.2) + 256MiB, computed in bytes so no rounding can undershoot.
	// The guard keeps the multiplication from wrapping on an absurd size.
	if expectedSizeBytes > (1<<62)/6 {
		return "", biz.ErrImportWorkerConfiguration
	}
	scaled := (expectedSizeBytes*6 + 4) / 5
	needed := resource.NewQuantity(scaled+256<<20, resource.BinarySI)
	if needed.Cmp(floor) <= 0 {
		return w.pvcSize, nil
	}
	return needed.String(), nil
}

// ensureJob creates the suspended Job. The plan is embedded as an annotation
// digest plus a mounted JSON, so the pod cannot be handed a different plan than
// the row that authorized it.
func (w *ImporterWorkload) ensureJob(ctx context.Context, attempt biz.DatasetImportAttempt, plan biz.AttemptPlan, task biz.DatasetImportTask) (biz.ImportWorkload, error) {
	labels := importerLabels(task, attempt)
	// The importer ServiceAccount mounts no pull reference: bootstrap forbids it
	// and the platform's node-level registry access covers this image.
	pullSecrets := []any{}
	expected := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{
			"name": attempt.JobName, "namespace": attempt.NamespaceName, "labels": labels,
			"annotations": importerAnnotations(task, attempt),
		},
		"spec": map[string]any{
			"suspend": true, "backoffLimit": int64(0),
			// The attempt's absolute deadline is frozen at allocation. A reused
			// attempt can already be past it, and the API server rejects a
			// negative activeDeadlineSeconds outright, so the value is clamped to
			// a small positive window instead of going negative. An expired
			// attempt is refused earlier by the scheduler, not here.
			"activeDeadlineSeconds": activeDeadlineSeconds(plan.DeadlineAt),
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels, "annotations": importerAnnotations(task, attempt)},
				"spec": map[string]any{
					"serviceAccountName":           w.serviceAccount,
					"automountServiceAccountToken": false,
					"restartPolicy":                "Never",
					// The stage volume is RWO block storage owned by root by
					// default. The image runs as 10001 and needs to create its
					// working directory, so the kubelet must hand the volume over
					// to that group; without it every download fails to stage.
					"securityContext":  map[string]any{"fsGroup": int64(10001), "fsGroupChangePolicy": "OnRootMismatch"},
					"imagePullSecrets": pullSecrets,
					"containers": []any{map[string]any{
						"name": "importer", "image": w.importerImage,
						"command": []any{"/ani-modeldev-importer"},
						"args":    []any{"--plan=/etc/modeldev-import/plan.json", "--work-dir=/workspace/work/import", "--report=/workspace/reports/import/result.json"},
						"env": []any{
							map[string]any{"name": "ANI_S3_ENDPOINT", "value": w.s3Endpoint},
							map[string]any{"name": "ANI_S3_REGION", "value": w.s3Region},
							map[string]any{"name": "ANI_HF_ENDPOINT", "value": w.huggingFaceURL},
							map[string]any{"name": "ANI_MS_ENDPOINT", "value": w.modelScopeURL},
							map[string]any{"name": "ANI_NAMESPACE_UID", "value": attempt.NamespaceUID},
							// The store uses an internal CA; trusting only the system roots
							// would fail every upload with an opaque TLS error.
							map[string]any{"name": "AWS_CA_BUNDLE", "value": "/etc/modeldev-import-ca/s3-ca.crt"},
						},
						"resources": map[string]any{"requests": map[string]any{"cpu": w.cpuRequest, "memory": w.memoryRequest},
							"limits": map[string]any{"cpu": w.cpuLimit, "memory": w.memoryLimit}},
						"volumeMounts": []any{
							map[string]any{"name": "plan", "mountPath": "/etc/modeldev-import", "readOnly": true},
							map[string]any{"name": "workspace", "mountPath": "/workspace"},
							map[string]any{"name": "credentials", "mountPath": "/var/run/modeldev-import-credentials", "readOnly": true},
							map[string]any{"name": "s3-ca", "mountPath": "/etc/modeldev-import-ca", "readOnly": true},
						},
					}},
					"volumes": []any{
						map[string]any{"name": "plan", "secret": map[string]any{
							"secretName": attempt.SecretName,
							"items":      []any{map[string]any{"key": "plan.json", "path": "plan.json"}},
						}},
						map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": attempt.PVCName}},
						map[string]any{"name": "credentials", "secret": map[string]any{"secretName": attempt.SecretName}},
						map[string]any{"name": "s3-ca", "configMap": map[string]any{"name": "modeldev-dataset-import-ca", "items": []any{map[string]any{"key": "s3-ca.pem", "path": "s3-ca.crt"}}}},
					},
				},
			},
		},
	}}
	created, err := w.ensureObject(ctx, importerJobResource, expected, false)
	if err != nil {
		return biz.ImportWorkload{}, err
	}
	return biz.ImportWorkload{JobUID: string(created.GetUID()), ResourceVersion: created.GetResourceVersion(), Created: true}, nil
}

// ensureObject creates the object or verifies that the existing one carries the
// same owner identity. It never patches a differing object.
func (w *ImporterWorkload) ensureObject(ctx context.Context, gvr schema.GroupVersionResource, expected *unstructured.Unstructured, secret bool) (*unstructured.Unstructured, error) {
	namespace, name := expected.GetNamespace(), expected.GetName()
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	actual, err := w.kube.Resource(gvr).Namespace(namespace).Get(call, name, metav1.GetOptions{})
	cancel()
	if apierrors.IsNotFound(err) {
		call, cancel = context.WithTimeout(ctx, 5*time.Second)
		actual, err = w.kube.Resource(gvr).Namespace(namespace).Create(call, expected, metav1.CreateOptions{})
		cancel()
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) {
			// The API server's own message names the denied field or policy.
			return nil, fmt.Errorf("%w: create %s/%s: %v", biz.ErrImportDispatchUnavailable, gvr.Resource, name, err)
		}
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("%w: create %s/%s: %v", biz.ErrImportDispatchUnavailable, gvr.Resource, name, err)
		}
		if apierrors.IsAlreadyExists(err) {
			call, cancel = context.WithTimeout(ctx, 5*time.Second)
			actual, err = w.kube.Resource(gvr).Namespace(namespace).Get(call, name, metav1.GetOptions{})
			cancel()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read %s/%s: %v", biz.ErrImportDispatchUnavailable, gvr.Resource, name, err)
	}
	if err := sameImporterIdentity(actual, expected); err != nil {
		return nil, err
	}
	_ = secret
	return actual, nil
}

// sameImporterIdentity rejects an object this attempt does not own. The check is
// on the labels that pin tenant/task/attempt, never on mutable spec fields.
func sameImporterIdentity(actual, expected *unstructured.Unstructured) error {
	labels := expected.GetLabels()
	for key, value := range labels {
		if actual.GetLabels()[key] != value {
			return biz.ErrImportStateConflict
		}
	}
	return nil
}

// ResumeImporter unsuspends exactly the observed Job UID.
func (w *ImporterWorkload) ResumeImporter(ctx context.Context, attempt biz.DatasetImportAttempt, jobUID string) error {
	if jobUID == "" {
		return biz.ErrImportStateConflict
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"suspend": false}})
	if err != nil {
		return fmt.Errorf("%w: encode resume patch: %v", biz.ErrPersistence, err)
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current, err := w.kube.Resource(importerJobResource).Namespace(attempt.NamespaceName).Get(call, attempt.JobName, metav1.GetOptions{})
	if err != nil {
		return biz.ErrImportDispatchUnavailable
	}
	if string(current.GetUID()) != jobUID {
		return biz.ErrImportStateConflict
	}
	call, cancel = context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := w.kube.Resource(importerJobResource).Namespace(attempt.NamespaceName).Patch(
		call, attempt.JobName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return biz.ErrImportDispatchUnavailable
	}
	return nil
}

// ObserveImporter reports a bounded status for the attempt's Job.
func (w *ImporterWorkload) ObserveImporter(ctx context.Context, attempt biz.DatasetImportAttempt) (biz.ImportWorkloadStatus, error) {
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current, err := w.kube.Resource(importerJobResource).Namespace(attempt.NamespaceName).Get(call, attempt.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return biz.ImportWorkloadStatus{Present: false}, nil
	}
	if err != nil {
		return biz.ImportWorkloadStatus{}, biz.ErrImportDispatchUnavailable
	}
	status := biz.ImportWorkloadStatus{Present: true, JobUID: string(current.GetUID())}
	if attempt.JobUID != "" && status.JobUID != attempt.JobUID {
		return biz.ImportWorkloadStatus{}, biz.ErrImportStateConflict
	}
	succeeded, found, _ := unstructured.NestedInt64(current.Object, "status", "succeeded")
	if found && succeeded > 0 {
		status.Finished, status.Succeeded = true, true
	}
	failed, found, _ := unstructured.NestedInt64(current.Object, "status", "failed")
	if found && failed > 0 {
		status.Finished, status.Failed = true, true
	}
	if reason, found, _ := unstructured.NestedString(current.Object, "metadata", "labels", "modeldev.ani.io/close-reason"); found {
		status.FailureReason = reason
	}
	return status, nil
}

// DeleteImporter removes the attempt-owned objects. Missing objects are not an
// error: cleanup must be idempotent across restarts.
func (w *ImporterWorkload) DeleteImporter(ctx context.Context, attempt biz.DatasetImportAttempt) error {
	cascade := metav1.DeletePropagationBackground
	for _, target := range []schema.GroupVersionResource{importerJobResource, importerPVCResource, importerSecretResource} {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := w.kube.Resource(target).Namespace(attempt.NamespaceName).Delete(call, importerObjectName(target, attempt), metav1.DeleteOptions{PropagationPolicy: &cascade})
		cancel()
		if err != nil && !apierrors.IsNotFound(err) {
			return biz.ErrImportDispatchUnavailable
		}
	}
	return nil
}

func base64Std(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

// minimumDeadlineSeconds keeps the Job spec valid even when the frozen deadline
// has already passed. The scheduler fails an expired attempt before dispatching.
const minimumDeadlineSeconds int64 = 60

func activeDeadlineSeconds(deadline time.Time) int64 {
	remaining := int64(time.Until(deadline).Seconds())
	if remaining < minimumDeadlineSeconds {
		return minimumDeadlineSeconds
	}
	return remaining
}

func importerObjectName(gvr schema.GroupVersionResource, attempt biz.DatasetImportAttempt) string {
	switch gvr {
	case importerPVCResource:
		return attempt.PVCName
	case importerSecretResource:
		return attempt.SecretName
	}
	return attempt.JobName
}

var _ biz.DatasetImportWorkloadControl = (*ImporterWorkload)(nil)

var _ = errors.Is
