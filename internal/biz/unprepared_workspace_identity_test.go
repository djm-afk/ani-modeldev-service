package biz_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The original 5f CreatePVC observation is emitted by Kubernetes' native
// timestamp decoder, then reloaded from JSON runtime facts before step cleanup.
func TestUnpreparedWorkspaceIdentitySurvivesNativeTimestampPersistence(t *testing.T) {
	pvc := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"creationTimestamp": "2026-10-29T00:29:19Z"}}}
	observed := biz.UnpreparedWorkspace{
		NamespaceName: "ani-modeldev-tenant-a-v21", NamespaceUID: "94f3afc0-7548-4099-9e11-3fa7fdf6cc67",
		PVCName: "ani-kfp-workspace-5f0b866e-31d1-4b44-8a7c-5cc900767e00", PVCUID: "c604601e-dfb4-486f-b4bc-43c759713f21",
		PVCResourceVersion: "12354210", SpecSHA256: "e24ea37b6ef5b80c234b2f2bfeadeaabffded89297f825d4edbd599b4e069d71",
		CreateTaskID: "67f6a5e2-5693-442c-b3ea-8f840654def9", CreatePodUID: "84050c60-a9c9-4f31-8b0b-69912e2e8e0e",
		RequestSHA256: "5eec28d340af5e8603b72f01034dbbb3a927255e995d5215dcff864eac94425d", CreatedAt: pvc.GetCreationTimestamp().Time.In(time.FixedZone("container-local-utc", 0)),
	}
	// A zero-offset local zone reproduces the scratch container independently
	// of the test host's timezone, while retaining native Kubernetes decoding.
	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	var stored biz.UnpreparedWorkspace
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.Equal(observed.CreatedAt) {
		t.Fatal("native and persisted PVC creation instants differ")
	}
	stored.PVCResourceVersion = "12354211" // Kubernetes bookkeeping is an observation, not a new volume.
	if !biz.SameUnpreparedWorkspace(&stored, &observed) {
		t.Fatal("UNPREPARED_WORKSPACE_IDENTITY: same original PVC instant and frozen identity cannot resume cleanup after JSON persistence")
	}
	otherOffset := observed
	otherOffset.CreatedAt = observed.CreatedAt.In(time.FixedZone("observer-offset", 8*60*60))
	if !biz.SameUnpreparedWorkspace(&stored, &otherOffset) {
		t.Fatal("same original PVC creation instant changed identity with the observer's UTC offset")
	}
	for _, test := range []struct {
		name   string
		change func(*biz.UnpreparedWorkspace)
	}{
		{"different PVC UID", func(v *biz.UnpreparedWorkspace) { v.PVCUID = "c604601e-dfb4-486f-b4bc-43c759713f22" }},
		{"different spec digest", func(v *biz.UnpreparedWorkspace) {
			v.SpecSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"different create Pod UID", func(v *biz.UnpreparedWorkspace) { v.CreatePodUID = "84050c60-a9c9-4f31-8b0b-69912e2e8e0f" }},
		{"different create task", func(v *biz.UnpreparedWorkspace) { v.CreateTaskID = "67f6a5e2-5693-442c-b3ea-8f840654def8" }},
		{"different request digest", func(v *biz.UnpreparedWorkspace) {
			v.RequestSHA256 = "1111111111111111111111111111111111111111111111111111111111111111"
		}},
		{"different creation instant", func(v *biz.UnpreparedWorkspace) { v.CreatedAt = v.CreatedAt.Add(time.Nanosecond) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := observed
			test.change(&changed)
			if biz.SameUnpreparedWorkspace(&stored, &changed) {
				t.Fatal("changed original workspace identity accepted")
			}
		})
	}
}
