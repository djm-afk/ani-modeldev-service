// Package trainer owns ModelDev's outbound Kubeflow Trainer operations.
package trainer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

var (
	ErrInvalidBinding     = errors.New("INVALID_TRAINING_RESOURCE_BINDING")
	ErrBindingMismatch    = errors.New("TRAINING_RESOURCE_BINDING_MISMATCH")
	ErrInvalidObservation = errors.New("INVALID_TRAINING_OBSERVATION")
)

// Adapter uses the official Kubernetes client supplied by the composition root.
// No product transport is connected to this initial test-first slice.
type Adapter struct {
	client      dynamic.Interface
	ownerSigner *OwnerAssertionSigner
}

func New(client dynamic.Interface) *Adapter {
	return &Adapter{client: client}
}

func (a *Adapter) WithOwnerSigner(signer *OwnerAssertionSigner) *Adapter {
	a.ownerSigner = signer
	return a
}

func (a *Adapter) ObserveTrainJob(ctx context.Context, binding biz.TrainJobBinding) (biz.TrainJobObservation, error) {
	if !validBinding(binding) {
		return biz.TrainJobObservation{}, ErrInvalidBinding
	}
	if a == nil || a.client == nil {
		return biz.TrainJobObservation{}, ErrInvalidObservation
	}
	namespace, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, binding.Namespace, metav1.GetOptions{})
	if err != nil {
		return biz.TrainJobObservation{}, fmt.Errorf("observe bound namespace: %w", err)
	}
	if namespace.GetAPIVersion() != "v1" || namespace.GetKind() != "Namespace" || namespace.GetName() != binding.Namespace || string(namespace.GetUID()) != binding.NamespaceUID {
		return biz.TrainJobObservation{}, ErrBindingMismatch
	}
	resource := schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}
	job, err := a.client.Resource(resource).Namespace(binding.Namespace).Get(ctx, binding.Name, metav1.GetOptions{})
	if err != nil {
		return biz.TrainJobObservation{}, fmt.Errorf("observe bound TrainJob: %w", err)
	}
	annotations := job.GetAnnotations()
	if job.GetAPIVersion() != "trainer.kubeflow.org/v1alpha1" || job.GetKind() != "TrainJob" || job.GetNamespace() != binding.Namespace || job.GetName() != binding.Name || string(job.GetUID()) != binding.UID ||
		!strings.EqualFold(annotations["modeldev.ani.io/tenant-id"], binding.TenantID) ||
		!strings.EqualFold(annotations["modeldev.ani.io/execution-id"], binding.ExecutionID) ||
		annotations["modeldev.ani.io/execution-spec-sha256"] != binding.SpecSHA256 {
		return biz.TrainJobObservation{}, ErrBindingMismatch
	}
	observation := biz.TrainJobObservation{
		NamespaceUID: string(namespace.GetUID()),
		TrainJobUID:  string(job.GetUID()),
		Generation:   job.GetGeneration(),
		Suspended:    biz.TrainingConditionUnknown,
		Complete:     biz.TrainingConditionUnknown,
		Failed:       biz.TrainingConditionUnknown,
		ObservedAt:   time.Now().UTC(),
	}
	if observation.Generation <= 0 {
		return biz.TrainJobObservation{}, ErrInvalidObservation
	}
	conditions, _, err := unstructured.NestedSlice(job.Object, "status", "conditions")
	if err != nil {
		return biz.TrainJobObservation{}, ErrInvalidObservation
	}
	seen := make(map[string]bool)
	for _, value := range conditions {
		fields, ok := value.(map[string]interface{})
		if !ok {
			return biz.TrainJobObservation{}, ErrInvalidObservation
		}
		var condition metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(fields, &condition); err != nil {
			return biz.TrainJobObservation{}, ErrInvalidObservation
		}
		var target *biz.TrainingConditionStatus
		var metadata *biz.TrainingConditionMetadata
		switch condition.Type {
		case "Suspended":
			target = &observation.Suspended
			metadata = &observation.SuspendedMetadata
		case "Complete":
			target = &observation.Complete
			metadata = &observation.CompleteMetadata
		case "Failed":
			target = &observation.Failed
			metadata = &observation.FailedMetadata
		default:
			continue
		}
		if seen[condition.Type] {
			return biz.TrainJobObservation{}, ErrInvalidObservation
		}
		seen[condition.Type] = true
		if condition.Status != metav1.ConditionTrue && condition.Status != metav1.ConditionFalse && condition.Status != metav1.ConditionUnknown {
			return biz.TrainJobObservation{}, ErrInvalidObservation
		}
		generation, present, err := unstructured.NestedInt64(fields, "observedGeneration")
		if err != nil || generation < 0 {
			return biz.TrainJobObservation{}, ErrInvalidObservation
		}
		// Trainer may omit this field or copy it from a JobSet condition. Preserve
		// the report without comparing generations from potentially different
		// resources. Pod success and writer absence still need independent proof.
		*target = biz.TrainingConditionStatus(condition.Status)
		*metadata = biz.TrainingConditionMetadata{Present: true, ObservedGeneration: generation, HasObservedGeneration: present}
	}
	if observation.Complete == biz.TrainingConditionTrue && observation.Failed == biz.TrainingConditionTrue {
		return biz.TrainJobObservation{}, ErrInvalidObservation
	}
	return observation, nil
}

func validBinding(binding biz.TrainJobBinding) bool {
	for _, value := range []string{binding.TenantID, binding.ExecutionID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != strings.ToLower(value) {
			return false
		}
	}
	for _, uid := range []string{binding.NamespaceUID, binding.UID} {
		if uid == "" || len(uid) > 128 || strings.TrimSpace(uid) != uid {
			return false
		}
	}
	if len(validation.IsDNS1123Label(binding.Namespace)) != 0 || len(validation.IsDNS1035Label(binding.Name)) != 0 {
		return false
	}
	if len(binding.SpecSHA256) != 64 || strings.ToLower(binding.SpecSHA256) != binding.SpecSHA256 {
		return false
	}
	_, err := hex.DecodeString(binding.SpecSHA256)
	return err == nil
}
