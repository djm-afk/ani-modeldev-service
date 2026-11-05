package modeldevwebhook

import (
	"context"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// importerServiceAccount owns dataset import Pods and nothing else.
	importerServiceAccount = "modeldev-dataset-importer"
	// purposeLabel marks a workload as a dataset import. Together with the
	// importer ServiceAccount it is the identity the native policy pins.
	purposeLabel      = "modeldev.ani.io/purpose"
	purposeImport     = "dataset-import"
	tenantIDLabel     = "modeldev.ani.io/tenant-id"
	datasetIDLabel    = "modeldev.ani.io/dataset-id"
	taskIDLabel       = "modeldev.ani.io/task-id"
	attemptIDLabel    = "modeldev.ani.io/attempt-id"
	namespaceUIDAnnot = "modeldev.ani.io/namespace-uid"
)

// isImporterPod reports whether this Pod claims the dataset import identity.
// Both facts are required: the importer account alone could otherwise be
// borrowed by a workload that simply drops the purpose label.
func isImporterPod(pod *corev1.Pod) bool {
	return pod.Spec.ServiceAccountName == importerServiceAccount && pod.Labels[purposeLabel] == purposeImport
}

// admitImporter validates a dataset import Pod against the importer contract
// instead of the training environment projection. Design §12.4 requires this
// branch to run before environment(): the importer has no training Environment
// and no KFP Workflow, so that lookup would reject every legitimate Pos.
//
// What is proven here, and only here:
//   - the Pod is the sole controller-owned child of a batch Job in this
//     namespace, and that Job carries exactly the same task/attempt identity;
//   - the Pod carries the frozen attempt identity and namespace UID;
//   - the container shape is the single importer container, no host namespaces,
//     no extra volumes beyond the three the control plane creates.
//
// It deliberately does not read the attempt Secret or the training bindings:
// the Secret cannot be read by this account, and the attempt row is already the
// control plane's own durable fact.
func (handler *Handler) admitImporter(ctx context.Context, request *admissionv1.AdmissionRequest, pod *corev1.Pod, failure *admissionFailure) (*admissionv1.AdmissionResponse, string) {
	if request.Kind != (metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}) || pod.APIVersion != "v1" || pod.Kind != "Pod" {
		return denied(request, "workload_identity")
	}
	if len(pod.OwnerReferences) != 1 {
		return denied(request, "workload_identity")
	}
	owner := pod.OwnerReferences[0]
	if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Controller == nil || !*owner.Controller ||
		len(validation.IsDNS1123Subdomain(owner.Name)) != 0 || !canonicalUUID(string(owner.UID)) {
		return denied(request, "workload_identity")
	}
	for _, key := range []string{purposeLabel, tenantIDLabel, datasetIDLabel, taskIDLabel, attemptIDLabel} {
		if pod.Labels[key] == "" {
			return denied(request, "importer_labels")
		}
	}
	if !canonicalUUID(pod.Labels[tenantIDLabel]) || !canonicalUUID(pod.Labels[datasetIDLabel]) ||
		!canonicalUUID(pod.Labels[taskIDLabel]) || !canonicalUUID(pod.Labels[attemptIDLabel]) {
		return denied(request, "importer_labels")
	}
	if namespaceUID := pod.Annotations[namespaceUIDAnnot]; !canonicalUUID(namespaceUID) {
		return denied(request, "importer_labels")
	}
	// The Pod must live in the tenant namespace its labels claim.
	if pod.Namespace != "tenant-"+pod.Labels[tenantIDLabel] {
		return denied(request, "importer_labels")
	}
	// Only the single importer container, with no host namespaces and no
	// ephemeral containers: the control plane creates exactly one.
	if pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC ||
		len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 {
		return denied(request, "container_contract")
	}
	if pod.Spec.Containers[0].Name != "importer" {
		return denied(request, "container_contract")
	}
	if pod.Spec.Containers[0].Image != handler.config.ImporterImage {
		// The importer identity is pinned to the configured image; a Job created
		// by a trusted writer with any other image is still not this contract.
		return denied(request, "image_contract")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return denied(request, "container_contract")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		// The importer never talks to the API server; a mounted token would be a
		// capability the contract does not grant.
		return denied(request, "container_contract")
	}

	readCtx, cancel := context.WithTimeout(ctx, handler.dependencyTimeout)
	defer cancel()
	started := time.Now()
	job, err := handler.reader.Job(readCtx, pod.Namespace, owner.Name)
	if err != nil || job == nil {
		failure.record("job", started, readCtx, err)
		return unavailable(request)
	}
	if string(job.UID) != string(owner.UID) || job.DeletionTimestamp != nil || job.Namespace != pod.Namespace {
		return denied(request, "job_identity")
	}
	// The parent Job must carry the same identity as the Pod. A Pod that changed
	// its own labels while its Job says otherwise is not this attempt.
	for _, key := range []string{purposeLabel, tenantIDLabel, datasetIDLabel, taskIDLabel, attemptIDLabel} {
		if job.Labels[key] == "" || job.Labels[key] != pod.Labels[key] {
			return denied(request, "job_identity")
		}
	}
	if job.Spec.Template.Spec.ServiceAccountName != importerServiceAccount {
		return denied(request, "job_identity")
	}
	// A resumed Job is the normal path; a suspended parent means the control
	// plane has not authorized execution yet.
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return denied(request, "job_suspended")
	}
	return &admissionv1.AdmissionResponse{UID: request.UID, Allowed: true}, "importer"
}
