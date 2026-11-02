package tenantenvironment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ktesting "k8s.io/client-go/testing"
)

func TestEnvironmentResourcesUseNativePermissionsWithoutGlobalInventory(t *testing.T) {
	p, row, kube := fixture(t)
	kube.PrependReactor("*", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		resource, verb := action.GetResource().Resource, action.GetVerb()
		unneeded := resource == "clusterrolebindings" || resource == "roles" || verb == "list"
		if resource == "clusterroles" {
			name := action.(ktesting.GetAction).GetName()
			unneeded = true
			for _, fixed := range []string{"modeldev-runtime-v1", "modeldev-step-v1", "modeldev-webhook-read-v1", "modeldev-kfp-api-v1", "modeldev-kfp-access-v1"} {
				if name == fixed {
					unneeded = false
				}
			}
		}
		if unneeded {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: resource}, "", nil)
		}
		return false, nil, nil
	})
	if err := p.Resources(context.Background(), row, func() error { return nil }); err != nil {
		t.Fatalf("native-permitted bootstrap must not require global authorization inventory: %v", err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "list" || action.GetResource().Resource == "clusterrolebindings" || action.GetResource().Resource == "roles" {
			t.Fatalf("environment required unrelated RBAC inventory: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestEnvironmentMissingOriginalIdentityRemainsConflict(t *testing.T) {
	p, row, kube := fixture(t)
	ctx := context.Background()
	if err := p.Resources(ctx, row, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	row.ExperimentID = "33333333-4444-4555-8666-777777777777"
	raw, err := p.Seal(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	row.BindingCanonical, row.BindingDigest = raw, biz.EnvironmentDigest(raw)
	if err = p.Publish(ctx, row, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	binding, err := projection.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = kube.Tracker().Delete(resource("ServiceAccount"), row.NamespaceName, "modeldev-step"); err != nil {
		t.Fatal(err)
	}
	kube.ClearActions()
	err = p.VerifyBinding(ctx, row.TenantID, binding.Snapshot(row.BindingDigest))
	var failure *biz.EnvironmentFailure
	if !errors.As(err, &failure) || failure.Reason != "ENVIRONMENT_OBJECT_CONFLICT" || !failure.Review {
		t.Fatalf("missing original SA must retain its identity-conflict result: %v", err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("verification recreated or altered the missing original identity")
		}
	}
}

func TestEnvironmentPlatformConfigRejectsRetiredAuthorizationInputs(t *testing.T) {
	p, _, _ := fixture(t)
	p.config.SchemaVersion = "ani.modeldev.tenant-environment-platform.v1"
	p.config.KFPEndpoint = "https://kfp.example.test"
	p.config.KFPCAFile = "/etc/modeldev/kfp-ca.crt"
	p.config.ArtifactBucketIsolationEvidenceSHA256 = strings.Repeat("a", 64)
	raw, err := json.Marshal(p.config)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "platform.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(file, biz.EnvironmentDigest(raw)); err != nil {
		t.Fatalf("platform must not require a second authorization configuration: %v", err)
	}
	for key, value := range map[string]any{
		"authorization_modes":                 []string{"Node", "RBAC"},
		"authorization_modes_evidence_sha256": strings.Repeat("a", 64),
		"trusted_principals":                  []map[string]string{{"kind": "ServiceAccount", "name": "modeldev-control", "namespace": "ani-system"}},
		"trusted_principals_evidence_sha256":  strings.Repeat("a", 64),
	} {
		t.Run(key, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			object[key] = value
			candidate, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(file, candidate, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadConfig(file, biz.EnvironmentDigest(candidate)); err == nil {
				t.Fatal("retired authorization input was accepted")
			}
		})
	}
}

func TestEnvironmentNativeAPIDenialStopsFurtherRequests(t *testing.T) {
	for _, test := range []struct {
		name, verb, resource, reason string
		denial                       error
		rotation                     bool
	}{
		{name: "public-create-forbidden", verb: "create", resource: "serviceaccounts", reason: "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", denial: apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "modeldev-step", nil)},
		{name: "public-create-unauthorized", verb: "create", resource: "serviceaccounts", reason: "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", denial: apierrors.NewUnauthorized("original service account credential refused")},
		{name: "public-create-admission-rejected", verb: "create", resource: "serviceaccounts", reason: "INCOMPATIBLE_ADMISSION_POLICY", denial: apierrors.NewInvalid(schema.GroupKind{Kind: "ServiceAccount"}, "modeldev-step", field.ErrorList{field.Forbidden(field.NewPath("metadata"), "ModelDev admission rejected")})},
		{name: "artifact-create-forbidden", verb: "create", resource: "secrets", reason: "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", denial: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "modeldev-kfp-artifacts", nil)},
		{name: "bootstrap-read-unauthorized", verb: "get", resource: "clusterroles", reason: "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", denial: apierrors.NewUnauthorized("original service account credential refused")},
		{name: "public-read-forbidden", verb: "get", resource: "serviceaccounts", reason: "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", denial: apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "modeldev-step", nil)},
		{name: "rotation-admission-rejected", verb: "patch", resource: "secrets", rotation: true, reason: "INCOMPATIBLE_ADMISSION_POLICY", denial: apierrors.NewInvalid(schema.GroupKind{Kind: "Secret"}, "modeldev-kfp-artifacts", field.ErrorList{field.Forbidden(field.NewPath("data"), "ModelDev admission rejected")})},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, row, kube := fixture(t)
			if test.rotation {
				if err := p.Resources(context.Background(), row, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p.config.ArtifactAccessKeyFile, []byte("new-value-denied-by-native-admission"), 0600); err != nil {
					t.Fatal(err)
				}
				kube.ClearActions()
			}
			deniedAt := -1
			requests := 0
			kube.PrependReactor("*", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				requests++
				if action.GetVerb() == test.verb && action.GetResource().Resource == test.resource {
					deniedAt = requests
					return true, nil, test.denial
				}
				return false, nil, nil
			})
			err := p.Resources(context.Background(), row, func() error { return nil })
			var failure *biz.EnvironmentFailure
			if !errors.As(err, &failure) || failure.Reason != test.reason {
				t.Fatalf("native denial must remain an explicit finite failure: %v", err)
			}
			if deniedAt < 0 || requests != deniedAt {
				t.Fatalf("native refusal was followed by another cluster request: deniedAt=%d actions=%d", deniedAt, requests)
			}
		})
	}
}
