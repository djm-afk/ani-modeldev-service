package biz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTenantEnvironmentNotRegistered = errors.New("ENVIRONMENT_NOT_REGISTERED")
	ErrTenantEnvironmentInvalid       = errors.New("INVALID_TENANT_ENVIRONMENT")
	ErrTenantEnvironmentPersistence   = errors.New("TENANT_ENVIRONMENT_PERSISTENCE")
)

type EnvironmentState string
type EnvironmentPhase string

const (
	EnvironmentProvisioning EnvironmentState = "PROVISIONING"
	EnvironmentReady        EnvironmentState = "READY"
	EnvironmentBlocked      EnvironmentState = "BLOCKED"
	EnvironmentNeedsReview  EnvironmentState = "NEEDS_REVIEW"
	EnvironmentNamespace    EnvironmentPhase = "NAMESPACE"
	EnvironmentResources    EnvironmentPhase = "RESOURCES"
	EnvironmentExperiment   EnvironmentPhase = "EXPERIMENT"
	EnvironmentVerify       EnvironmentPhase = "VERIFY"
	EnvironmentPublish      EnvironmentPhase = "PUBLISH"
	EnvironmentComplete     EnvironmentPhase = "COMPLETE"
)

// EnvironmentObjectReceipt is deliberately finite. Secrets retain identity and
// key schema only; their data and data digests never enter persistent receipts.
type EnvironmentObjectReceipt struct {
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	UID        string    `json:"uid,omitempty"`
	SpecDigest string    `json:"spec_digest"`
	Confirmed  bool      `json:"confirmed"`
	CheckedAt  time.Time `json:"checked_at"`
}
type EnvironmentExperimentIntent struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Description string `json:"description"`
	BodyDigest  string `json:"body_digest"`
	Sent        bool   `json:"sent"`
}
type EnvironmentReceipts struct {
	Objects     []EnvironmentObjectReceipt   `json:"objects"`
	Materials   []EnvironmentMaterialReceipt `json:"materials"`
	ImageSecret string                       `json:"image_secret,omitempty"`
}
type EnvironmentMaterialReceipt struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Parent     string    `json:"parent"`
	SpecDigest string    `json:"spec_digest"`
	CheckedAt  time.Time `json:"checked_at"`
}
type TenantExecutionEnvironment struct {
	ID, TenantID, ClusterID, NamespaceName, NamespaceUID          string
	State                                                         EnvironmentState
	Phase                                                         EnvironmentPhase
	ProvisionSpecCanonical                                        []byte
	ProvisionSpecHash                                             string
	BindingCanonical                                              []byte
	BindingDigest, KFPConnectionRef, ExperimentName, ExperimentID string
	ExperimentIntent                                              *EnvironmentExperimentIntent
	Receipts                                                      EnvironmentReceipts
	FirstOperationID, LastOperationID, FirstActorID, LastActorID  string
	ReasonCode, ReasonStage                                       string
	RowVersion                                                    int64
	CreatedAt, UpdatedAt, LastVerifiedAt, ReadyAt                 time.Time
}
type EnvironmentFailure struct {
	Reason    string
	Review    bool
	Uncertain bool
}

func (e *EnvironmentFailure) Error() string { return e.Reason }
func ValidEnvironmentReason(reason string) bool {
	switch reason {
	case "TENANT_NAMESPACE_NOT_FOUND", "TENANT_NAMESPACE_ACCESS_DENIED", "ENVIRONMENT_NOT_REGISTERED", "ENVIRONMENT_PROVISIONING", "COMPUTE_CLUSTER_MISMATCH", "TENANT_NAMESPACE_OWNERSHIP_MISMATCH", "TENANT_NAMESPACE_NOT_ACTIVE", "TENANT_NAMESPACE_UID_CHANGED", "ENVIRONMENT_OBJECT_CONFLICT", "ENVIRONMENT_CONFIG_DRIFT", "KFP_AUTHENTICATION_FAILED", "KFP_PERMISSION_DENIED", "KFP_EXPERIMENT_CONFLICT", "KFP_EXPERIMENT_RESULT_UNCERTAIN", "KFP_ARTIFACT_CONFIGURATION_NOT_READY", "ENVIRONMENT_AUTHORIZATION_UNVERIFIED", "KFP_ARTIFACT_ACCESS_NOT_ISOLATED", "FIXED_PIPELINE_MATERIAL_MISMATCH", "TENANT_IMAGE_SPACE_NOT_READY", "MANAGED_IMAGE_REGISTRY_MISMATCH", "INCOMPATIBLE_ADMISSION_POLICY", "ENVIRONMENT_DEPENDENCY_UNAVAILABLE":
		return true
	}
	return false
}
func EnvironmentError(reason string) error { return &EnvironmentFailure{Reason: reason} }
func EnvironmentConflict(reason string) error {
	return &EnvironmentFailure{Reason: reason, Review: true}
}
func EnvironmentUncertain() error {
	return &EnvironmentFailure{Reason: "KFP_EXPERIMENT_RESULT_UNCERTAIN", Uncertain: true}
}

type TenantEnvironmentRepository interface {
	Get(context.Context, string, string) (TenantExecutionEnvironment, error)
	Register(context.Context, TenantExecutionEnvironment) (TenantExecutionEnvironment, error)
	TryLock(context.Context, string, string) (TenantEnvironmentLock, bool, error)
	Save(context.Context, *TenantExecutionEnvironment) error
}
type TenantEnvironmentLock interface{ Release() }

// TenantEnvironmentPlatform owns finite external operations. Every creation is
// preceded by a saved typed intent through the supplied checkpoint function.
type TenantEnvironmentPlatform interface {
	ProvisionSpec() ([]byte, error)
	VerifyNamespace(context.Context, *TenantExecutionEnvironment) error
	Resources(context.Context, *TenantExecutionEnvironment, func() error) error
	Experiment(context.Context, *TenantExecutionEnvironment, func() error) error
	Verify(context.Context, *TenantExecutionEnvironment, func() error) error
	Seal(context.Context, *TenantExecutionEnvironment) ([]byte, error)
	Publish(context.Context, *TenantExecutionEnvironment, func() error) error
	VerifyPublication(context.Context, *TenantExecutionEnvironment) error
}
type TenantEnvironment struct {
	repository               TenantEnvironmentRepository
	platform                 TenantEnvironmentPlatform
	clusterID, connectionRef string
}

func NewTenantEnvironment(repository TenantEnvironmentRepository, platform TenantEnvironmentPlatform, clusterID, connectionRef string) (*TenantEnvironment, error) {
	if repository == nil || platform == nil || !CanonicalEnvironmentUUID(clusterID) || connectionRef == "" {
		return nil, ErrTenantEnvironmentInvalid
	}
	return &TenantEnvironment{repository: repository, platform: platform, clusterID: clusterID, connectionRef: connectionRef}, nil
}
func CanonicalEnvironmentUUID(s string) bool {
	id, e := uuid.Parse(s)
	return e == nil && id != uuid.Nil && id.String() == s
}
func TenantNamespace(tenant string) (string, error) {
	if !CanonicalEnvironmentUUID(tenant) {
		return "", ErrTenantEnvironmentInvalid
	}
	return "tenant-" + tenant, nil
}
func TenantEnvironmentLockKey(tenant, cluster string) int64 {
	h := sha256.Sum256([]byte("ani.modeldev.tenant-environment.v1\x00" + tenant + "\x00" + cluster))
	return int64(binary.BigEndian.Uint64(h[:8]))
}
func EnvironmentDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (u *TenantEnvironment) Get(ctx context.Context, tenant string) (TenantExecutionEnvironment, error) {
	if u == nil || !CanonicalEnvironmentUUID(tenant) {
		return TenantExecutionEnvironment{}, ErrTenantEnvironmentInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return u.repository.Get(ctx, tenant, u.clusterID)
}
func (u *TenantEnvironment) Ensure(ctx context.Context, tenant, actor, operation string) (TenantExecutionEnvironment, error) {
	if u == nil || !CanonicalEnvironmentUUID(tenant) || !CanonicalEnvironmentUUID(operation) || actor == "" {
		return TenantExecutionEnvironment{}, ErrTenantEnvironmentInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	canonical, err := u.platform.ProvisionSpec()
	if err != nil {
		return TenantExecutionEnvironment{}, err
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	row, err := u.repository.Register(ctx, TenantExecutionEnvironment{ID: id, TenantID: tenant, ClusterID: u.clusterID, NamespaceName: "tenant-" + tenant, State: EnvironmentProvisioning, Phase: EnvironmentNamespace, ProvisionSpecCanonical: canonical, ProvisionSpecHash: EnvironmentDigest(canonical), KFPConnectionRef: u.connectionRef, ExperimentName: "md-env-" + id, FirstOperationID: operation, LastOperationID: operation, FirstActorID: actor, LastActorID: actor, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return row, err
	}
	lock, held, err := u.repository.TryLock(ctx, tenant, u.clusterID)
	if err != nil {
		return row, err
	}
	if !held {
		return u.repository.Get(ctx, tenant, u.clusterID)
	}
	defer lock.Release()
	row, err = u.repository.Get(ctx, tenant, u.clusterID)
	if err != nil {
		return row, err
	}
	row.LastOperationID = operation
	row.LastActorID = actor
	checkpoint := func() error { return u.repository.Save(ctx, &row) }
	fail := func(cause error) (TenantExecutionEnvironment, error) {
		if ctx.Err() != nil {
			return row, ctx.Err()
		}
		var f *EnvironmentFailure
		if !errors.As(cause, &f) {
			f = &EnvironmentFailure{Reason: "ENVIRONMENT_DEPENDENCY_UNAVAILABLE"}
		}
		if !ValidEnvironmentReason(f.Reason) {
			f = &EnvironmentFailure{Reason: "ENVIRONMENT_DEPENDENCY_UNAVAILABLE"}
		}
		row.State = EnvironmentBlocked
		if f.Review {
			row.State = EnvironmentNeedsReview
		}
		if f.Uncertain {
			row.State = EnvironmentProvisioning
		}
		row.ReasonCode = f.Reason
		row.ReasonStage = string(row.Phase)
		if e := checkpoint(); e != nil {
			return row, e
		}
		return row, nil
	}
	if row.ProvisionSpecHash != EnvironmentDigest(canonical) || string(row.ProvisionSpecCanonical) != string(canonical) {
		return fail(EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT"))
	}
	// Every explicit call rechecks the original namespace, including READY rows.
	if err = u.platform.VerifyNamespace(ctx, &row); err != nil {
		row.Phase = EnvironmentNamespace
		return fail(err)
	}
	if err = checkpoint(); err != nil {
		return row, err
	}
	row.State = EnvironmentProvisioning
	row.ReasonCode = ""
	row.ReasonStage = ""
	steps := []struct {
		phase EnvironmentPhase
		run   func() error
	}{
		{EnvironmentResources, func() error { return u.platform.Resources(ctx, &row, checkpoint) }},
		{EnvironmentExperiment, func() error { return u.platform.Experiment(ctx, &row, checkpoint) }},
		{EnvironmentVerify, func() error { return u.platform.Verify(ctx, &row, checkpoint) }},
		{EnvironmentPublish, func() error {
			binding, e := u.platform.Seal(ctx, &row)
			if e != nil {
				return e
			}
			if len(row.BindingCanonical) > 0 && string(row.BindingCanonical) != string(binding) {
				return EnvironmentConflict("ENVIRONMENT_CONFIG_DRIFT")
			}
			row.BindingCanonical = binding
			row.BindingDigest = EnvironmentDigest(binding)
			if e = checkpoint(); e != nil {
				return e
			}
			return u.platform.Publish(ctx, &row, checkpoint)
		}},
	}
	for _, step := range steps {
		row.Phase = step.phase
		if err = checkpoint(); err != nil {
			return row, err
		}
		if err = step.run(); err != nil {
			return fail(err)
		}
	}
	if err = u.platform.VerifyPublication(ctx, &row); err != nil {
		return fail(err)
	}
	row.State = EnvironmentReady
	row.Phase = EnvironmentComplete
	row.ReasonCode = ""
	row.ReasonStage = ""
	row.LastVerifiedAt = time.Now().UTC()
	if row.ReadyAt.IsZero() {
		row.ReadyAt = row.LastVerifiedAt
	}
	if err = checkpoint(); err != nil {
		return row, err
	}
	return row, nil
}

// DecodeEnvironmentReceipts rejects unrecognized recovery state.
func DecodeEnvironmentReceipts(raw []byte) (EnvironmentReceipts, error) {
	var v EnvironmentReceipts
	if len(raw) > 512<<10 {
		return v, ErrTenantEnvironmentPersistence
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, ErrTenantEnvironmentPersistence
	}
	if d.Decode(new(any)) != io.EOF {
		return v, ErrTenantEnvironmentPersistence
	}
	allowed := map[string]string{"modeldev-step": "ServiceAccount|RoleBinding", "trainer-workload": "ServiceAccount", "verifier": "ServiceAccount", "modeldev-runtime": "RoleBinding", "modeldev-webhook-read": "RoleBinding", "modeldev-kfp-api": "RoleBinding", "modeldev-kfp-access": "RoleBinding", "modeldev-step-owner": "ConfigMap", "ani-kfp-ca": "ConfigMap", "kfp-launcher": "ConfigMap", "metadata-grpc-configmap": "ConfigMap", "modeldev-execution-environment": "ConfigMap", "modeldev-kfp-artifacts": "Secret"}
	seen := map[string]bool{}
	for _, r := range v.Objects {
		key := r.Kind + "/" + r.Name
		if !strings.Contains("|"+allowed[r.Name]+"|", "|"+r.Kind+"|") || seen[key] || len(r.SpecDigest) != 64 || strings.Trim(r.SpecDigest, "0123456789abcdef") != "" || r.UID != "" && !CanonicalEnvironmentUUID(r.UID) || r.Confirmed && (r.UID == "" || r.CheckedAt.IsZero()) {
			return v, ErrTenantEnvironmentPersistence
		}
		seen[key] = true
	}
	if len(v.Materials) > 4 {
		return v, ErrTenantEnvironmentPersistence
	}
	for _, r := range v.Materials {
		if r.Kind != "PipelineVersion" || !CanonicalEnvironmentUUID(r.ID) || !CanonicalEnvironmentUUID(r.Parent) || len(r.SpecDigest) != 64 || strings.Trim(r.SpecDigest, "0123456789abcdef") != "" || r.CheckedAt.IsZero() {
			return v, ErrTenantEnvironmentPersistence
		}
	}
	return v, nil
}
