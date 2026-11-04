package dataset

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// NamespaceReader verifies the standard tenant Namespace and returns its real
// identity. It is the KFP-free core the training path also uses.
type NamespaceReader interface {
	ReadNamespaceIdentity(ctx context.Context, tenantID, originalUID string) (NamespaceIdentity, error)
}

// NamespaceIdentity mirrors the tenantenvironment fact without importing that
// package's richer types.
type NamespaceIdentity struct {
	ClusterID string
	Name      string
	UID       string
}

// EnvironmentObserver reads the tenant execution registry, read-only. It exists
// so a NEEDS_REVIEW hold or a pinned original UID can block new imports without
// the dataset path depending on the training admission reader.
type EnvironmentObserver interface {
	ObserveEnvironment(ctx context.Context, tenantID, clusterID string) (EnvironmentObservation, error)
}

// EnvironmentObservation is the subset of registry state a dataset import cares
// about. A missing row is a normal, non-blocking state; it grants nothing and
// revokes nothing.
type EnvironmentObservation struct {
	Present      bool
	State        string
	NamespaceUID string
	Blocked      bool
	BlockReason  string
}

// RegistryObservationFailed reports a registry read that could not answer. It
// must not be treated as "no row".
var ErrRegistryObservationFailed = errors.New("IMPORT_ENVIRONMENT_MISSING")

// TargetReader composes the standard Namespace, the platform input scope and the
// registry observation into one frozen target.
type TargetReader struct {
	namespaces   NamespaceReader
	environments EnvironmentObserver
	platform     cpup01.StorageScope
	clusterID    string
}

func NewTargetReader(namespaces NamespaceReader, environments EnvironmentObserver, platform cpup01.StorageScope, clusterID string) (*TargetReader, error) {
	if namespaces == nil || platform.Validate() != nil || !biz.CanonicalEnvironmentUUID(clusterID) {
		return nil, biz.ErrImportEnvironmentMissing
	}
	return &TargetReader{namespaces: namespaces, environments: environments, platform: platform, clusterID: clusterID}, nil
}

var _ biz.DatasetImportTargetReader = (*TargetReader)(nil)

// ReadDatasetImportTarget builds the target for one tenant. Ordering matters:
// the registry observation is read after the Namespace so an UID change is seen
// against the live identity rather than a stale one.
func (reader *TargetReader) ReadDatasetImportTarget(ctx context.Context, tenantID string) (biz.DatasetImportTarget, error) {
	if reader == nil || !biz.CanonicalEnvironmentUUID(tenantID) {
		return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
	}
	if err := ctx.Err(); err != nil {
		return biz.DatasetImportTarget{}, err
	}
	identity, err := reader.namespaces.ReadNamespaceIdentity(ctx, tenantID, "")
	if err != nil {
		// Namespace problems are environment missing, not authorization.
		return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
	}
	if identity.ClusterID != reader.clusterID {
		return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
	}
	if reader.environments != nil {
		observed, err := reader.environments.ObserveEnvironment(ctx, tenantID, identity.ClusterID)
		if err != nil {
			return biz.DatasetImportTarget{}, ErrRegistryObservationFailed
		}
		if observed.Present {
			// A pinned original UID must still match the live Namespace.
			if observed.NamespaceUID != "" && observed.NamespaceUID != identity.UID {
				return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
			}
			// NEEDS_REVIEW blocks new work; the reason stays with the environment.
			if observed.Blocked {
				return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
			}
		}
	}
	scope, err := cpup01.TenantStorageScope(reader.platform, tenantID, "input")
	if err != nil {
		return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
	}
	target := biz.DatasetImportTarget{
		Schema:        biz.DatasetImportTargetSchema,
		ClusterID:     identity.ClusterID,
		NamespaceName: identity.Name,
		NamespaceUID:  identity.UID,
		InputScope: biz.ImportTargetScope{
			StorageConnectionID: scope.StorageConnectionID,
			Bucket:              scope.Bucket,
			ApprovedPrefix:      scope.ApprovedPrefix,
			CredentialReference: scope.CredentialReference,
		},
	}
	targetDigest, err := target.Digest()
	if err != nil {
		return biz.DatasetImportTarget{}, biz.ErrImportEnvironmentMissing
	}
	target.SnapshotSHA256 = targetDigest
	target.InputScopeSHA256 = scopeDigest(scope)
	return target, nil
}

func scopeDigest(scope cpup01.StorageScope) string {
	target := biz.DatasetImportTarget{InputScope: biz.ImportTargetScope{
		StorageConnectionID: scope.StorageConnectionID, Bucket: scope.Bucket,
		ApprovedPrefix: scope.ApprovedPrefix, CredentialReference: scope.CredentialReference,
	}}
	// The scope digest covers only the four scope fields, so it is stable while
	// the namespace identity changes.
	return biz.ImportTargetScopeDigest(target.InputScope)
}
