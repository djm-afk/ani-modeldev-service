package biz

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeDatasetNameAcceptsLegalNames(t *testing.T) {
	for _, raw := range []string{"客服问答", "support-alpaca", "A", strings.Repeat("名", 128)} {
		if got, err := NormalizeDatasetName(raw); err != nil || got != raw {
			t.Fatalf("%q rejected or rewritten: %q %v", raw, got, err)
		}
	}
}

func TestNormalizeDatasetNameRejectsIllegalNames(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"too long", strings.Repeat("a", 129)},
		{"leading space", " name"},
		{"trailing space", "name "},
		{"control", "na\x00me"},
		{"newline", "na\nme"},
		{"tab", "na\tme"},
		// A decomposed name would change under NFC; accepting it would make
		// "the same name" depend on which form the client sent.
		{"not NFC", "e\u0301"},
	} {
		if _, err := NormalizeDatasetName(tc.raw); !errors.Is(err, ErrDatasetInvalidMetadata) {
			t.Fatalf("%s: %q accepted", tc.name, tc.raw)
		}
	}
}

func TestNormalizeDatasetNameIsCaseSensitive(t *testing.T) {
	lower, err1 := NormalizeDatasetName("dataset")
	upper, err2 := NormalizeDatasetName("DATASET")
	if err1 != nil || err2 != nil || lower == upper {
		t.Fatal("case variants collapsed into one name")
	}
}

func TestNormalizeDatasetDescription(t *testing.T) {
	if got, err := NormalizeDatasetDescription("第一行\n第二行"); err != nil || got != "第一行\n第二行" {
		t.Fatalf("newline rejected: %q %v", got, err)
	}
	if _, err := NormalizeDatasetDescription(""); err != nil {
		t.Fatal("empty description rejected")
	}
	if _, err := NormalizeDatasetDescription(strings.Repeat("字", 4001)); !errors.Is(err, ErrDatasetInvalidMetadata) {
		t.Fatal("oversize description accepted")
	}
	for _, bad := range []string{"a\x00b", "a\rb", "a\tb"} {
		if _, err := NormalizeDatasetDescription(bad); !errors.Is(err, ErrDatasetInvalidMetadata) {
			t.Fatalf("unsafe control accepted: %q", bad)
		}
	}
}

func TestValidIdempotencyKey(t *testing.T) {
	for _, ok := range []string{"a", "create-support-dataset-01", "a.b_c:d-e", strings.Repeat("k", 128)} {
		if !ValidIdempotencyKey(ok) {
			t.Fatalf("legal key rejected: %q", ok)
		}
	}
	for _, bad := range []string{"", "-leading", ".leading", "_leading", ":leading", "with space", "with/slash", strings.Repeat("k", 129), "中文"} {
		if ValidIdempotencyKey(bad) {
			t.Fatalf("illegal key accepted: %q", bad)
		}
	}
}

func TestCommandDigestDistinguishesIntentButIgnoresNonIntent(t *testing.T) {
	base := DatasetCommand{
		TenantID: "11111111-1111-4111-8111-111111111111", ActorID: "governance:user:1",
		Kind: OpUpdateDataset, DatasetID: "22222222-2222-4222-8222-222222222222",
		Name: "new-name", UpdateName: true, ExpectedRevision: 3,
	}
	if base.Digest() != base.Digest() {
		t.Fatal("digest is not deterministic")
	}
	// Non-intent fields must not move the digest: the same command replayed
	// with a different idempotency key or trace is still the same intent.
	other := base
	other.IdempotencyKey = "another-key"
	if other.Digest() != base.Digest() {
		t.Fatal("idempotency key leaked into the intent digest")
	}
	// Intent fields must move it.
	for name, change := range map[string]func(*DatasetCommand){
		"name":     func(c *DatasetCommand) { c.Name = "other" },
		"revision": func(c *DatasetCommand) { c.ExpectedRevision = 4 },
		"object":   func(c *DatasetCommand) { c.DatasetID = "33333333-3333-4333-8333-333333333333" },
		"kind":     func(c *DatasetCommand) { c.Kind = OpArchiveDataset },
	} {
		changed := base
		change(&changed)
		if changed.Digest() == base.Digest() {
			t.Fatalf("%s did not change the intent digest", name)
		}
	}
}

func TestCommandReceiptSameIntent(t *testing.T) {
	command := DatasetCommand{Kind: OpCreateDataset, DatasetID: "22222222-2222-4222-8222-222222222222", Name: "n", UpdateName: true}
	receipt := CommandReceipt{Kind: command.Kind, RequestSHA256: command.Digest()}
	if !receipt.SameIntent(command) {
		t.Fatal("identical intent was not recognized")
	}
	other := command
	other.Name = "different"
	if receipt.SameIntent(other) {
		t.Fatal("a different intent matched an existing receipt")
	}
}

func TestDatasetImportTargetDigestIsStableAndScoped(t *testing.T) {
	target := DatasetImportTarget{
		ClusterID:     "22222222-2222-4222-8222-222222222222",
		NamespaceName: "tenant-11111111-1111-4111-8111-111111111111",
		NamespaceUID:  "33333333-3333-4333-8333-333333333333",
		InputScope: ImportTargetScope{
			StorageConnectionID: "input-store-v1", Bucket: "inputs",
			ApprovedPrefix: "inputs/11111111-1111-4111-8111-111111111111", CredentialReference: "input-read",
		},
	}
	first, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := target.Digest()
	if first != second || len(first) != 64 {
		t.Fatalf("digest unstable or malformed: %q %q", first, second)
	}
	// Any frozen field moving changes the identity: a retry must never silently
	// re-point at another namespace or scope.
	for name, change := range map[string]func(*DatasetImportTarget){
		"namespace uid": func(t *DatasetImportTarget) { t.NamespaceUID = "44444444-4444-4444-8444-444444444444" },
		"cluster":       func(t *DatasetImportTarget) { t.ClusterID = "55555555-5555-4555-8555-555555555555" },
		"bucket":        func(t *DatasetImportTarget) { t.InputScope.Bucket = "other" },
		"prefix": func(t *DatasetImportTarget) {
			t.InputScope.ApprovedPrefix = "inputs/66666666-6666-4666-8666-666666666666"
		},
		"credential": func(t *DatasetImportTarget) { t.InputScope.CredentialReference = "other" },
	} {
		changed := target
		change(&changed)
		if digest, err := changed.Digest(); err == nil && digest == first {
			t.Fatalf("%s did not change the target digest", name)
		}
	}
}

func TestDatasetImportTargetDigestRejectsNonCanonicalTenant(t *testing.T) {
	target := DatasetImportTarget{
		ClusterID:  "22222222-2222-4222-8222-222222222222",
		InputScope: ImportTargetScope{ApprovedPrefix: "inputs/not-a-uuid"},
	}
	if _, err := target.Digest(); !errors.Is(err, ErrDatasetInvalidMetadata) {
		t.Fatalf("non-UUID tenant prefix accepted: %v", err)
	}
}

func TestOperationKindValidity(t *testing.T) {
	for _, kind := range []OperationKind{OpCreateDataset, OpUpdateDataset, OpArchiveDataset, OpRestoreDataset, OpImportVersion, OpCancelImport, OpRetryImport} {
		if !ValidOperationKind(kind) {
			t.Fatalf("%q rejected", kind)
		}
	}
	if ValidOperationKind("SOMETHING") || ValidOperationKind("") {
		t.Fatal("unknown operation kind accepted")
	}
}
