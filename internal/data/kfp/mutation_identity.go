package kfp

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// WithMutationIdentity installs the cluster connection used for exact original
// object GETs immediately before KFP writes. Read-only KFP evidence stays usable
// when a current environment is not READY or its Namespace has disappeared.
func (client *Client) WithMutationIdentity(identity dynamic.Interface) *Client {
	client.identity = identity
	return client
}
func (client *Client) verifyMutationNamespace(ctx context.Context, plan biz.PipelineDispatchPlan, creating bool) error {
	if client == nil || client.identity == nil {
		return ErrManagedRunUnverified
	}
	e := plan.Environment
	ns, err := client.identity.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, e.NamespaceName, metav1.GetOptions{})
	if err != nil || ns.GetName() != e.NamespaceName || string(ns.GetUID()) != e.NamespaceUID || ns.GetLabels()["network.ani.io/tenant-id"] != plan.TenantID || ns.GetLabels()["network.ani.io/managed-by"] != "ani-network-service" {
		return ErrManagedRunUnverified
	}
	if creating {
		phase, _, err := unstructured.NestedString(ns.Object, "status", "phase")
		if err != nil || phase != "Active" || ns.GetDeletionTimestamp() != nil {
			return ErrManagedRunUnverified
		}
	}
	return nil
}
func (client *Client) verifyMutationWorkflow(ctx context.Context, plan biz.PipelineDispatchPlan, authority biz.RunAuthorityCandidate) error {
	if err := client.verifyMutationNamespace(ctx, plan, false); err != nil {
		return err
	}
	workflow, err := client.identity.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(plan.Environment.NamespaceName).Get(ctx, authority.WorkflowName, metav1.GetOptions{})
	if err != nil || string(workflow.GetUID()) != authority.WorkflowUID || workflow.GetLabels()["pipeline/runid"] != authority.RunID || workflow.GetNamespace() != plan.Environment.NamespaceName {
		return ErrManagedRunUnverified
	}
	return nil
}
