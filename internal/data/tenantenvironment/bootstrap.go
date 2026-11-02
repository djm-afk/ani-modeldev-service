package tenantenvironment

import (
	"context"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (p *Platform) verifyBootstrap(ctx context.Context) error {
	roles := map[string]bool{"modeldev-runtime-v1": true, "modeldev-step-v1": true, "modeldev-webhook-read-v1": true, "modeldev-kfp-api-v1": true, "modeldev-kfp-access-v1": true}
	counts := map[string]int{}
	policies := map[string]bool{"modeldev-bootstrap-v1": true, "modeldev-environment-config-v1": true, "modeldev-workload-guard-v1": true}
	names := map[string]bool{}
	for _, pin := range p.config.Bootstrap {
		key := pin.Kind + "/" + pin.Name
		if names[key] || !biz.CanonicalEnvironmentUUID(pin.UID) || !validDigest(pin.SHA256) {
			return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
		}
		names[key] = true
		switch pin.Kind {
		case "ClusterRole":
			if !roles[pin.Name] {
				return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
			}
			delete(roles, pin.Name)
		case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding":
			policyName := strings.TrimSuffix(pin.Name, "-binding")
			if !policies[policyName] || (pin.Kind == "ValidatingAdmissionPolicyBinding" && pin.Name != policyName+"-binding") {
				return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
			}
		case "MutatingWebhookConfiguration":
			if pin.Name != "ani-modeldev-webhook" {
				return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
			}
		default:
			return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
		}
		counts[pin.Kind]++
		o, e := p.get(ctx, resource(pin.Kind), "", pin.Name)
		if e != nil {
			if apierrors.IsNotFound(e) {
				return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
			}
			return kubernetesFailure(e)
		}
		if string(o.GetUID()) != pin.UID || objectDigest(o) != pin.SHA256 {
			return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
		}
		if pin.Kind == "ValidatingAdmissionPolicy" {
			observed, ok, _ := unstructured.NestedInt64(o.Object, "status", "observedGeneration")
			_, checked, _ := unstructured.NestedMap(o.Object, "status", "typeChecking")
			warnings, _, _ := unstructured.NestedSlice(o.Object, "status", "typeChecking", "expressionWarnings")
			if !ok || observed != o.GetGeneration() || observed < 1 || !checked || len(warnings) > 0 {
				return biz.EnvironmentError("INCOMPATIBLE_ADMISSION_POLICY")
			}
		}
	}
	if len(roles) != 0 || counts["ValidatingAdmissionPolicy"] != 3 || counts["ValidatingAdmissionPolicyBinding"] != 3 || counts["MutatingWebhookConfiguration"] != 1 {
		return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
	}
	return nil
}
