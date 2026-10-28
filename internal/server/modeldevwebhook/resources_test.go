package modeldevwebhook_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestComponentResourcesComeFromImmutableMaterialRoleWithoutHiddenMaximum(t *testing.T) {
	config, reader := fixtureConfig(t)
	material := conformance.MaterialV21()
	for _, role := range cpup01.ComponentRoles("GENERAL_TRAINING") {
		material.ComponentResources[role] = cpup01.ComponentResources{CPURequest: "1250m", CPULimit: "2500m", MemoryRequest: "3Gi", MemoryLimit: "9Gi", EphemeralStorageRequest: "17Gi", EphemeralStorageLimit: "21Gi"}
	}
	raw, err := material.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "immutable-material.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(raw)
	config.Profiles[0].MaterialFile = file
	config.Profiles[0].MaterialSHA256 = hex.EncodeToString(sha[:])
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	body := request(t, config, "cpu-v1", "prepare")
	changePod(t, &body, func(pod *corev1.Pod) {
		pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1250m"), corev1.ResourceMemory: resource.MustParse("3Gi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2500m"), corev1.ResourceMemory: resource.MustParse("9Gi")}}
	})
	result := call(t, server, body)
	if !result.Allowed {
		t.Fatalf("explicit material resources refused: %+v", result.Result)
	}
	patch, err := jsonpatch.DecodePatch(result.Patch)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := patch.Apply(body.Request.Object.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := json.Unmarshal(mutated, &pod); err != nil {
		t.Fatal(err)
	}
	main := pod.Spec.Containers[0]
	if main.Resources.Requests.StorageEphemeral().String() != "17Gi" || main.Resources.Limits.StorageEphemeral().String() != "21Gi" || main.Resources.Requests.Cpu().String() != "1250m" || main.Resources.Limits.Memory().String() != "9Gi" {
		t.Fatalf("role resources were replaced by code defaults: %+v", main.Resources)
	}
	changePod(t, &body, func(pod *corev1.Pod) {
		pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("8Gi")
	})
	if result := call(t, server, body); result.Allowed {
		t.Fatal("Pod resource mismatch hid a different compiled IR/material")
	}
}

func TestComponentResourceDigestAndRoleCacheMustMatchFrozenMaterial(t *testing.T) {
	config, reader := fixtureConfig(t)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	for _, role := range []string{"prepare", "collect", "publish"} {
		t.Run(role, func(t *testing.T) {
			body := request(t, config, "cpu-v1", role)
			changePod(t, &body, func(pod *corev1.Pod) { pod.Annotations["ani.modeldev/component-resources-sha256"] = "wrong" })
			if call(t, server, body).Allowed {
				t.Fatal("accepted an IR resource digest different from frozen material")
			}
			body = request(t, config, "cpu-v1", role)
			changePod(t, &body, func(pod *corev1.Pod) {
				pod.Spec.Volumes[0].EmptyDir.SizeLimit = resource.NewQuantity(1, resource.BinarySI)
			})
			if call(t, server, body).Allowed {
				t.Fatal("accepted a control directory size outside frozen resources")
			}
			body = request(t, config, "cpu-v1", role)
			changePod(t, &body, func(pod *corev1.Pod) { pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "TMPDIR", Value: "/tmp"}} })
			if call(t, server, body).Allowed {
				t.Fatal("accepted library temp files outside role PVC work")
			}
		})
	}
}
