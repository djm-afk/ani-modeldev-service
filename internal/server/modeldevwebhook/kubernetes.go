package modeldevwebhook

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type KubernetesReader struct {
	core    kubernetes.Interface
	dynamic dynamic.Interface
}

func NewKubernetesReader(core kubernetes.Interface, client dynamic.Interface) (*KubernetesReader, error) {
	if core == nil || client == nil {
		return nil, ErrConfiguration
	}
	return &KubernetesReader{core: core, dynamic: client}, nil
}

// NewInClusterReader uses only the Pod's projected service-account identity and
// the Kubernetes CA. It does not load a developer kubeconfig or other secrets.
func NewInClusterReader(timeout time.Duration) (*KubernetesReader, error) {
	if timeout < 50*time.Millisecond || timeout > 3*time.Second {
		return nil, ErrConfiguration
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, ErrConfiguration
	}
	config.Timeout, config.QPS, config.Burst, config.UserAgent = timeout, 20, 32, "ani-modeldev-webhook"
	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, ErrConfiguration
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, ErrConfiguration
	}
	return NewKubernetesReader(core, client)
}

func (reader *KubernetesReader) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return reader.core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

func (reader *KubernetesReader) Workflow(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return reader.dynamic.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

var _ IdentityReader = (*KubernetesReader)(nil)
