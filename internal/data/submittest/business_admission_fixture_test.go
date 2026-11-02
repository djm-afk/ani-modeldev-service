//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

func prepareMainFlowAdmission(t *testing.T, ctx context.Context, f *completeFixture, pool *pgxpool.Pool, store *s3.Client) (*service.Admission, cpup01.ComputeOrigin, cpup01.Intent) {
	t.Helper()
	f.ensureEnvironment(t, pool)
	snapshot := f.request.Admission.Snapshot
	// These are the external fixture's trusted tenant/storage bindings, not an
	// ENV approval or a client-supplied scope. READY still requires real reads,
	// the production CSV verifier and the production input transaction path.
	inputScope := cpup01.StorageScope{StorageConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, ApprovedPrefix: path.Dir(snapshot.Input.Object.Key)}
	request := biz.InputImport{Format: "CSV",
		TenantID: f.request.Admission.TenantID, RequestID: uuid.NewString(), InputVersionID: snapshot.Input.InputVersionID,
		Actor: f.request.Admission.Actor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
		Scope: inputScope, Object: snapshot.Input.Object,
	}
	inputs := input.New(pool)
	importer := biz.NewInputImporter(inputs, objectstore.NewVerifier(store, inputScope.StorageConnectionID), cpup01.ContentLimits{MaxContentBytes: 17179869184}, 1800*time.Second)
	if _, err := importer.ImportCSV(ctx, request); err != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: actual fixed CSV import failed: %v", err)
	}
	ready, err := inputs.Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || ready.State != biz.InputStateReady || ready.Verification == nil || ready.Failure != nil || ready.Verification.ValidateFor(request) != nil {
		t.Fatalf("BFF_ADMISSION_PREFLIGHT: verified CSV was not persisted READY: %v", err)
	}

	directory := f.environment.MaterialsDirectory
	facts, err := admissionfacts.New(tenantenvironment.NewRepository(pool), f.environment.Platform, catalogue.NewReader(directory), snapshot.Environment.ClusterID)
	if err != nil {
		t.Fatal("BFF_ADMISSION_PREFLIGHT: dynamic facts from real READY environment", err)
	}
	selection := snapshot.ComputeOrigin
	resolver := biz.NewAdmissionResolver(catalogue.NewReader(directory), inputs, facts, submissionImages{reference: f.image}, snapshot.Limits)
	t.Log("BFF_ADMISSION_READY: actual fixed CSV verified and persisted READY; canonical four-profile catalogue and real Ensure-sealed READY environment loaded")
	return service.NewAdmission(resolver), selection, f.request.Admission.Intent
}

// submissionImages is the synthetic catalogue answer for the boundary fixture.
type submissionImages struct{ reference string }

func (images submissionImages) ResolveTrainingImage(_ context.Context, _ string, id, scope string, platform cpup01.TrainingPlatform) (biz.TrainingImageResolution, error) {
	digest := strings.Split(images.reference, "@")[1]
	return biz.TrainingImageResolution{ImageID: id, Scope: scope, ImageVersion: 1, Reference: images.reference, Digest: digest, RegistryAuthority: cpup01.NormalizeRegistryAuthority(images.reference), Platform: platform}, nil
}

func (submissionImages) EnsurePullSecret(_ context.Context, tenant, cluster, namespace, _, registry string) (string, error) {
	return cpup01.PullSecretName(tenant, cluster, namespace, registry)
}
