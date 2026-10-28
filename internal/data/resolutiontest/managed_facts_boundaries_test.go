package resolutiontest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedFactsRequireAllFiniteTenantImplementationFields(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	for _, field := range []string{"schema_version", "resource_tenant_id", "implementation_id", "environment", "input_scope", "publication_scope", "timing", "workspace", "input_file_path", "output_directory_path", "environment_evidence", "application_evidence", "environment.namespace_uid", "environment.binding_digest", "environment.identities", "environment.identities.trainer_service_account", "input_scope.storage_connection_id", "input_scope.approved_prefix", "publication_scope.credential_reference", "workspace.capacity_bytes", "workspace.input_subpath", "environment_evidence.sha256", "timing.execution_timeout_default_seconds", "timing.execution_timeout_max_seconds", "timing.step_timeout_seconds"} {
		t.Run(field, func(t *testing.T) {
			source := pinManagedFactsBytes(t, editManagedFactsField(t, raw, strings.Split(field, "."), nil))
			requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{source}, admissionfacts.ErrInvalidFacts)
		})
	}
}
func TestManagedFactsRejectAmbiguousUntypedAndRetiredFields(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	replace := func(old, new string) []byte {
		if !bytes.Contains(raw, []byte(old)) {
			t.Fatal("mutation target absent", old)
		}
		return bytes.Replace(raw, []byte(old), []byte(new), 1)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"unknown root", replace(`{"schema_version":`, `{"current":true,"schema_version":`)},
		{"retired release", replace(`{"schema_version":`, `{"release_id":"old","schema_version":`)},
		{"unknown nested", replace(`"workspace":{`, `"workspace":{"verified":true,`)},
		{"duplicate root", replace(`{"schema_version":`, `{"implementation_id":"builtin.mlp","schema_version":`)},
		{"duplicate nested", replace(`"namespace_name":"cpu-contract-fixture"`, `"namespace_name":"cpu-contract-fixture","namespace_name":"cpu-contract-fixture"`)},
		{"case alias", replace(`"workspace":`, `"Workspace":`)},
		{"nested case alias", replace(`"capacity_bytes":`, `"Capacity_Bytes":`)},
		{"null", editManagedFactsField(t, raw, []string{"environment"}, json.RawMessage(`null`))},
		{"nested null", editManagedFactsField(t, raw, []string{"input_scope", "credential_reference"}, json.RawMessage(`null`))},
		{"wrong type", editManagedFactsField(t, raw, []string{"workspace", "capacity_bytes"}, json.RawMessage(`1073741824`))},
		{"zero timing", editManagedFactsField(t, raw, []string{"timing", "step_timeout_seconds"}, json.RawMessage(`0`))},
		{"trailing object", append(bytes.Clone(raw), []byte(`{}`)...)},
		{"invalid UTF8", replace("kfp-managed-v1", "kfp-\xff")},
		{"unpaired UTF16", replace("kfp-managed-v1", `kfp-\ud800`)},
		{"wrong schema", editManagedFactsField(t, raw, []string{"schema_version"}, json.RawMessage(`"old.v1"`))},
		{"zero tenant", editManagedFactsField(t, raw, []string{"resource_tenant_id"}, json.RawMessage(`"00000000-0000-0000-0000-000000000000"`))},
		{"credential URL", editManagedFactsField(t, raw, []string{"application_evidence", "reference"}, json.RawMessage(`"https://example.test/?token=private-fixture-token"`))},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, test.raw)}, admissionfacts.ErrInvalidFacts)
		})
	}
}
func TestManagedFactsPinsExactBytesBoundsSourcesAndRejectsDuplicateKeys(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	original := pinManagedFactsBytes(t, raw)
	changed := pinManagedFactsBytes(t, append(bytes.Clone(raw), '\n'))
	stale := changed
	stale.SHA256 = original.SHA256
	requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{stale}, admissionfacts.ErrInvalidFacts)
	if _, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{changed}); e != nil {
		t.Fatal("valid new byte pin rejected", e)
	}
	requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{original, changed}, admissionfacts.ErrInvalidFacts)
	requireManagedFactsLoadFailure(t, f.ctx, nil, admissionfacts.ErrInvalidFacts)
	requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, nil)}, admissionfacts.ErrInvalidFacts)
	maximum := append(bytes.Clone(raw), bytes.Repeat([]byte{' '}, cpup01.MaxMaterialBytes-len(raw))...)
	if _, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, maximum)}); e != nil {
		t.Fatal("valid source at current material limit rejected", e)
	}
	requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, append(maximum, ' '))}, admissionfacts.ErrInvalidFacts)
	sources := make([]admissionfacts.FileSource, 65)
	for n := range sources {
		tenant := json.RawMessage(fmt.Sprintf(`"11111111-2222-4333-8444-%012d"`, n+1))
		sources[n] = pinManagedFactsBytes(t, editManagedFactsField(t, raw, []string{"resource_tenant_id"}, tenant))
	}
	if _, e := admissionfacts.Load(f.ctx, sources[:64]); e != nil {
		t.Fatal(e)
	}
	requireManagedFactsLoadFailure(t, f.ctx, sources, admissionfacts.ErrInvalidFacts)
}
func TestManagedFactsRejectUnsafeActualFileKindsAndPaths(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	for _, kind := range []string{"missing", "directory", "symlink", "hardlink", "fifo", "unclean"} {
		t.Run(kind, func(t *testing.T) {
			source := pinManagedFactsBytes(t, raw)
			target := filepath.Join(t.TempDir(), "selected")
			want := admissionfacts.ErrInvalidFacts
			var e error
			switch kind {
			case "missing":
				want = admissionfacts.ErrFactsUnavailable
			case "directory":
				e = os.Mkdir(target, 0700)
			case "symlink":
				e = os.Symlink(source.Path, target)
				want = admissionfacts.ErrFactsUnavailable
			case "hardlink":
				e = os.Link(source.Path, target)
			case "fifo":
				e = syscall.Mkfifo(target, 0600)
			case "unclean":
				target = filepath.Dir(source.Path) + "/./" + filepath.Base(source.Path)
			}
			if e != nil {
				t.Fatal(e)
			}
			source.Path = target
			requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{source}, want)
		})
	}
}
func TestManagedFactsUsesExactTenantImplementationKeysAndIndependentCopies(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	otherTenant := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	other := editManagedFactsField(t, raw, []string{"resource_tenant_id"}, json.RawMessage(`"`+otherTenant+`"`))
	other = editManagedFactsField(t, other, []string{"environment", "namespace_name"}, json.RawMessage(`"other-tenant"`))
	fine := editManagedFactsField(t, raw, []string{"implementation_id"}, json.RawMessage(`"builtin.sft_lora"`))
	reader, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, raw), pinManagedFactsBytes(t, other), pinManagedFactsBytes(t, fine)})
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		tenant, id string
		want       error
	}{{f.request.IdentityRef.TenantID, "builtin.mlp", nil}, {otherTenant, "builtin.mlp", nil}, {f.request.IdentityRef.TenantID, "builtin.sft_lora", nil}, {otherTenant, "builtin.sft_lora", biz.ErrAdmissionEnvironmentNotReady}, {"invalid", "builtin.mlp", biz.ErrInvalidAdmission}, {f.request.IdentityRef.TenantID, "future.script", biz.ErrInvalidAdmission}} {
		got, e := reader.ReadAdmissionFacts(f.ctx, test.tenant, test.id)
		if !errors.Is(e, test.want) || (test.want != nil && !reflect.DeepEqual(got, biz.TenantAdmissionFacts{})) {
			t.Fatal("wrong key selected facts", e)
		}
	}
	facts, e := reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, "builtin.mlp")
	if e != nil {
		t.Fatal(e)
	}
	facts.Environment.Identities.TrainerServiceAccount = "caller-change"
	again, e := reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, "builtin.mlp")
	if e != nil || !reflect.DeepEqual(again, f.facts) {
		t.Fatal("caller changed pinned facts")
	}
}
func TestManagedFactsCancellationReturnsNoPartialReaderFactsOrCandidate(t *testing.T) {
	f := prepareResolution(t)
	source := writeManagedFactsFixture(t, f)
	reader, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{source})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	requireManagedFactsLoadFailure(t, ctx, []admissionfacts.FileSource{source}, context.Canceled)
	got, e := reader.ReadAdmissionFacts(ctx, f.request.IdentityRef.TenantID, "builtin.mlp")
	if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(got, biz.TenantAdmissionFacts{}) {
		t.Fatal("canceled lookup returned facts")
	}
	candidate, e := f.resolver.Prepare(ctx, f.request)
	if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(candidate, cpup01.PreparedCandidate{}) {
		t.Fatal("canceled Prepare returned candidate")
	}
	requireNoResolvedExecution(t, f)
}
func managedFixtureBytes(t *testing.T, fixture resolutionFixture) []byte {
	t.Helper()
	source := writeManagedFactsFixture(t, fixture)
	raw, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatalf("MANAGED_FACTS_PREFLIGHT: fixture read failed; behavior NOT_RUN: %v", err)
	}
	return raw
}

func pinManagedFactsBytes(t *testing.T, raw []byte) admissionfacts.FileSource {
	t.Helper()
	file := filepath.Join(t.TempDir(), "selected-facts.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatalf("MANAGED_FACTS_PREFLIGHT: fixture write failed; behavior NOT_RUN: %v", err)
	}
	digest := sha256.Sum256(raw)
	return admissionfacts.FileSource{Path: file, SHA256: hex.EncodeToString(digest[:])}
}

func editManagedFactsField(t *testing.T, raw []byte, fields []string, value json.RawMessage) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(fields) == 0 || object[fields[0]] == nil {
		t.Fatal("MANAGED_FACTS_PREFLIGHT: mutation path absent; behavior NOT_RUN")
	}
	if len(fields) > 1 {
		object[fields[0]] = editManagedFactsField(t, object[fields[0]], fields[1:], value)
	} else if value == nil {
		delete(object, fields[0])
	} else {
		object[fields[0]] = value
	}
	changed, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("MANAGED_FACTS_PREFLIGHT: fixture edit encoding failed; behavior NOT_RUN: %v", err)
	}
	return changed
}

func requireManagedFactsLoadFailure(t *testing.T, ctx context.Context, sources []admissionfacts.FileSource, want error) {
	t.Helper()
	type result struct {
		reader *admissionfacts.Reader
		err    error
	}
	done := make(chan result, 1)
	go func() {
		reader, err := admissionfacts.Load(ctx, sources)
		done <- result{reader: reader, err: err}
	}()
	select {
	case got := <-done:
		if !errors.Is(got.err, want) || got.reader != nil {
			t.Fatalf("unsafe/incomplete file set must fail atomically with no reader: got %v; want %v", got.err, want)
		}
		for _, source := range sources {
			if strings.Contains(got.err.Error(), source.Path) || strings.Contains(got.err.Error(), "private-fixture-token") {
				t.Fatal("load failure exposed a filesystem path or raw fixture credential")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded file loading blocked on an unsafe source")
	}
}

func TestManagedFactsRejectDifferentGlobalTimingByTenantOrImplementation(t *testing.T) {
	f := prepareResolution(t)
	raw := managedFixtureBytes(t, f)
	for _, field := range []string{"execution_timeout_default_seconds", "execution_timeout_max_seconds", "step_timeout_seconds"} {
		different := editManagedFactsField(t, raw, []string{"implementation_id"}, json.RawMessage(`"builtin.sft_lora"`))
		value := `901`
		if field == "execution_timeout_default_seconds" {
			value = `3601`
		}
		if field == "execution_timeout_max_seconds" {
			value = `7201`
		}
		different = editManagedFactsField(t, different, []string{"timing", field}, json.RawMessage(value))
		requireManagedFactsLoadFailure(t, f.ctx, []admissionfacts.FileSource{pinManagedFactsBytes(t, raw), pinManagedFactsBytes(t, different)}, admissionfacts.ErrInvalidFacts)
	}
}
