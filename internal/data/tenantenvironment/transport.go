package tenantenvironment

import (
	"bytes"
	"io"
	"net/http"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type boundedTransport struct{ next http.RoundTripper }

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	_ = response.Body.Close()
	if err != nil || len(raw) > 1<<20 {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	return response, nil
}

// BoundedKubernetesClient shares the already authenticated HTTP transport while
// bounding decoded API bytes before the Kubernetes decoder allocates objects.
func BoundedKubernetesClient(config *rest.Config, client *http.Client) (dynamic.Interface, error) {
	if config == nil || client == nil || client.Transport == nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	bounded := *client
	bounded.Transport = boundedTransport{next: client.Transport}
	return dynamic.NewForConfigAndClient(config, &bounded)
}
