package dataset

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// A tenant with no training registry row must still produce a workload identity
// the native policy accepts: the frozen attempt target, never an invented
// environment id (§7.3).
func TestImporterIdentityCarriesTheFrozenTargetWithoutARegistryRow(t *testing.T) {
	task := biz.DatasetImportTask{
		TenantID: "64c5c35a-f554-432c-b4bc-7b4f36f08420", DatasetID: "22222222-3333-4444-8555-666666666666",
		TaskID:               "77777777-8888-4999-8aaa-bbbbbbbbbbbb",
		TargetSnapshotSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	attempt := biz.DatasetImportAttempt{AttemptID: "99999999-aaaa-4bbb-8ccc-dddddddddddd", NamespaceUID: "a8d8a472-8a8a-43cf-abf0-0e805bbe67b0"}
	labels := importerLabels(task, attempt)
	for key, value := range map[string]string{
		"ani.io/managed-by":          "ani-modeldev-service",
		"modeldev.ani.io/purpose":    "dataset-import",
		"modeldev.ani.io/tenant-id":  task.TenantID,
		"modeldev.ani.io/dataset-id": task.DatasetID,
		"modeldev.ani.io/task-id":    task.TaskID,
		"modeldev.ani.io/attempt-id": attempt.AttemptID,
	} {
		if labels[key] != value {
			t.Fatalf("%s = %v, want %q", key, labels[key], value)
		}
	}
	if _, present := labels["modeldev.ani.io/environment-id"]; present {
		t.Fatal("a missing registry row invented an environment id")
	}
	annotations := importerAnnotations(task, attempt)
	if annotations["modeldev.ani.io/target-snapshot-sha256"] != task.TargetSnapshotSHA256 {
		t.Fatal("the frozen target digest is not on the workload")
	}
	if annotations["modeldev.ani.io/namespace-uid"] != attempt.NamespaceUID {
		t.Fatal("the live Namespace UID is not on the workload")
	}

	task.EnvironmentID = "430d97aa-bfd1-4a3e-9f59-6092c8315484"
	labels = importerLabels(task, attempt)
	if labels["modeldev.ani.io/environment-id"] != task.EnvironmentID {
		t.Fatal("an observed registry row was not recorded for audit")
	}
}
