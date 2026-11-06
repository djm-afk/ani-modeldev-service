package tenantenvironment

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The importer account and its artifact-store CA are Dataset material: their
// identity is the live Namespace and purpose, so they are identical for tenants
// with and without a training registry row (§7.3, §12.4).
func TestDatasetMaterialIgnoresTheTrainingRegistry(t *testing.T) {
	row := &biz.TenantExecutionEnvironment{
		ID: "430d97aa-bfd1-4a3e-9f59-6092c8315484", TenantID: "64c5c35a-f554-432c-b4bc-7b4f36f08420",
		ClusterID:     "f39bf6e2-f3ed-4191-ada3-caeebf86c220",
		NamespaceName: "tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420",
		NamespaceUID:  "a8d8a472-8a8a-43cf-abf0-0e805bbe67b0",
		Receipts:      biz.EnvironmentReceipts{ImageSecret: "ani-pull-0123456789abcdef0123456789abcdef"},
	}
	account := datasetImporterServiceAccount(row)
	if account.GetName() != "modeldev-dataset-importer" || account.GetNamespace() != row.NamespaceName {
		t.Fatalf("importer material identity = %s/%s", account.GetNamespace(), account.GetName())
	}
	labels := account.GetLabels()
	for key, value := range map[string]string{
		"ani.io/managed-by":         "ani-modeldev-service",
		"modeldev.ani.io/purpose":   "dataset-import",
		"modeldev.ani.io/tenant-id": row.TenantID,
	} {
		if labels[key] != value {
			t.Fatalf("%s = %q, want %q", key, labels[key], value)
		}
	}
	if _, present := labels["modeldev.ani.io/environment-id"]; present {
		t.Fatal("importer material depends on a training registry row")
	}
	if account.GetAnnotations()["modeldev.ani.io/namespace-uid"] != row.NamespaceUID {
		t.Fatal("importer material lost the live Namespace UID")
	}
	automount, found, err := unstructured.NestedBool(account.Object, "automountServiceAccountToken")
	if err != nil || !found || automount {
		t.Fatal("importer material must not mount an API token")
	}
	refs, found, err := unstructured.NestedSlice(account.Object, "imagePullSecrets")
	if err != nil || !found || len(refs) != 0 {
		t.Fatal("importer material must not carry pull references")
	}

	ca := datasetImporterCA(row, "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	if ca.GetName() != "modeldev-dataset-import-ca" || ca.GetNamespace() != row.NamespaceName {
		t.Fatalf("CA material identity = %s/%s", ca.GetNamespace(), ca.GetName())
	}
	if ca.GetLabels()["modeldev.ani.io/purpose"] != "dataset-import" {
		t.Fatal("CA material lost the dataset purpose")
	}
	if _, present := ca.GetLabels()["modeldev.ani.io/environment-id"]; present {
		t.Fatal("CA material depends on a training registry row")
	}
	data, found, err := unstructured.NestedStringMap(ca.Object, "data")
	if err != nil || !found || len(data) != 1 || data["s3-ca.pem"] == "" {
		t.Fatal("CA material must carry exactly the artifact store CA")
	}
}
