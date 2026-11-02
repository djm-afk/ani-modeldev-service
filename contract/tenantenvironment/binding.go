// Package tenantenvironment defines the immutable, non-sensitive namespace
// projection shared by the registry and admission readers.
package tenantenvironment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

const Schema = "ani.modeldev.tenant-execution-environment.v1"

var ErrInvalidBinding = errors.New("INVALID_TENANT_ENVIRONMENT_BINDING")

type Profile struct {
	ID             string `json:"id"`
	PipelineID     string `json:"pipeline_id"`
	VersionID      string `json:"version_id"`
	IRSHA256       string `json:"ir_sha256"`
	MaterialSHA256 string `json:"material_sha256"`
}
type ObjectRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	SpecDigest string `json:"spec_digest"`
}

// Environment excludes BindingDigest: the outer canonical byte digest is
// supplied only when converting to an execution snapshot.
type Environment struct {
	BindingID        string                     `json:"binding_id"`
	ClusterID        string                     `json:"cluster_id"`
	NamespaceName    string                     `json:"namespace_name"`
	NamespaceUID     string                     `json:"namespace_uid"`
	KFPConnectionRef string                     `json:"kfp_connection_ref"`
	ExperimentID     string                     `json:"experiment_id"`
	Identities       cpup01.RuntimeIdentityRefs `json:"identities"`
}
type Binding struct {
	SchemaVersion        string                            `json:"schema_version"`
	TenantID             string                            `json:"tenant_id"`
	Environment          Environment                       `json:"environment"`
	InputScope           cpup01.StorageScope               `json:"input_scope"`
	PublicationScope     cpup01.StorageScope               `json:"publication_scope"`
	ModelScope           cpup01.StorageScope               `json:"model_scope"`
	Workspace            cpup01.WorkspaceContract          `json:"workspace"`
	Timing               cpup01.TimingConfiguration        `json:"timing"`
	InputFilePath        string                            `json:"input_file_path"`
	OutputDirectoryPath  string                            `json:"output_directory_path"`
	TargetPlatform       cpup01.TrainingPlatform           `json:"target_platform"`
	ManagedImageAccess   cpup01.ManagedImageAccessSnapshot `json:"managed_image_access"`
	DispatchOwner        cpup01.DispatchOwnerSnapshot      `json:"dispatch_owner"`
	MaterialBundleDigest string                            `json:"material_bundle_digest"`
	Profiles             []Profile                         `json:"profiles"`
	ControllerPrincipals []string                          `json:"controller_principals"`
	Objects              []ObjectRef                       `json:"objects"`
}

func canonicalUUID(s string) bool {
	id, e := uuid.Parse(s)
	return e == nil && id != uuid.Nil && id.String() == s
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func (b Binding) Canonical() ([]byte, error) {
	e := b.Environment
	if b.SchemaVersion != Schema || !canonicalUUID(b.TenantID) || !canonicalUUID(e.BindingID) || !canonicalUUID(e.ClusterID) || !canonicalUUID(e.NamespaceUID) || !canonicalUUID(e.ExperimentID) || e.NamespaceName != "tenant-"+b.TenantID || e.KFPConnectionRef == "" || !digest(b.MaterialBundleDigest) || len(b.Profiles) != 4 || len(b.ControllerPrincipals) == 0 || b.InputScope.ApprovedPrefix != "inputs/"+b.TenantID || b.PublicationScope.ApprovedPrefix != "executions/"+b.TenantID || b.InputScope.Validate() != nil || b.PublicationScope.Validate() != nil {
		return nil, ErrInvalidBinding
	}
	if b.ModelScope.Validate() != nil || b.ModelScope.ApprovedPrefix != b.TenantID || b.ManagedImageAccess.Validate() != nil || b.TargetPlatform.Validate() != nil {
		return nil, ErrInvalidBinding
	}
	if b.Workspace.Validate() != nil || !path.IsAbs(b.InputFilePath) || path.Clean(b.InputFilePath) != b.InputFilePath || !path.IsAbs(b.OutputDirectoryPath) || path.Clean(b.OutputDirectoryPath) != b.OutputDirectoryPath {
		return nil, ErrInvalidBinding
	}
	if _, err := b.Timing.Resolve(time.Unix(0, 0).UTC(), nil); err != nil {
		return nil, ErrInvalidBinding
	}
	owner := b.DispatchOwner
	owner.PipelineRoot += "/11111111-1111-4111-8111-111111111111"
	if owner.ValidateFor(b.TenantID, "11111111-1111-4111-8111-111111111111") != nil {
		return nil, ErrInvalidBinding
	}
	if e.Identities.KFPStepServiceAccount != "modeldev-step" || e.Identities.TrainerServiceAccount != "trainer-workload" || e.Identities.VerifierServiceAccount != "verifier" || !canonicalUUID(e.Identities.KFPStepServiceAccountUID) || !canonicalUUID(e.Identities.TrainerServiceAccountUID) || !canonicalUUID(e.Identities.VerifierServiceAccountUID) {
		return nil, ErrInvalidBinding
	}
	if !sort.SliceIsSorted(b.Profiles, func(i, j int) bool { return b.Profiles[i].ID < b.Profiles[j].ID }) || !sort.StringsAreSorted(b.ControllerPrincipals) {
		return nil, ErrInvalidBinding
	}
	for i, p := range b.Profiles {
		if p.ID == "" || !canonicalUUID(p.PipelineID) || !canonicalUUID(p.VersionID) || !digest(p.IRSHA256) || !digest(p.MaterialSHA256) || (i > 0 && b.Profiles[i-1].ID == p.ID) {
			return nil, ErrInvalidBinding
		}
	}
	expectedObjects := map[string]bool{"ServiceAccount/modeldev-step": true, "ServiceAccount/trainer-workload": true, "ServiceAccount/verifier": true, "RoleBinding/modeldev-runtime": true, "RoleBinding/modeldev-step": true, "RoleBinding/modeldev-webhook-read": true, "RoleBinding/modeldev-kfp-api": true, "RoleBinding/modeldev-kfp-access": true, "ConfigMap/modeldev-step-owner": true, "ConfigMap/ani-kfp-ca": true, "ConfigMap/kfp-launcher": true, "ConfigMap/metadata-grpc-configmap": true}
	if len(b.Objects) != len(expectedObjects) {
		return nil, ErrInvalidBinding
	}
	uids := map[string]bool{}
	for i, o := range b.Objects {
		key := o.Kind + "/" + o.Name
		if !expectedObjects[key] || uids[o.UID] {
			return nil, ErrInvalidBinding
		}
		delete(expectedObjects, key)
		uids[o.UID] = true
		if o.Name == "modeldev-execution-environment" || o.Kind == "Secret" || o.Kind == "" || o.Name == "" || !canonicalUUID(o.UID) || !digest(o.SpecDigest) || (i > 0 && b.Objects[i-1].Kind+"/"+b.Objects[i-1].Name >= o.Kind+"/"+o.Name) {
			return nil, ErrInvalidBinding
		}
		if o.Kind == "ServiceAccount" {
			expected := map[string]string{"modeldev-step": e.Identities.KFPStepServiceAccountUID, "trainer-workload": e.Identities.TrainerServiceAccountUID, "verifier": e.Identities.VerifierServiceAccountUID}
			if o.UID != expected[o.Name] {
				return nil, ErrInvalidBinding
			}
		}
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw)+64 > 64<<10 {
		return nil, ErrInvalidBinding
	}
	return raw, nil
}
func Decode(raw []byte) (Binding, error) {
	var b Binding
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw)+64 > 64<<10 || d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF {
		return b, ErrInvalidBinding
	}
	canonical, err := b.Canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return b, ErrInvalidBinding
	}
	return b, nil
}
func (b Binding) Snapshot(bindingDigest string) cpup01.EnvironmentBindingSnapshot {
	e := b.Environment
	return cpup01.EnvironmentBindingSnapshot{BindingID: e.BindingID, BindingDigest: bindingDigest, ClusterID: e.ClusterID, NamespaceName: e.NamespaceName, NamespaceUID: e.NamespaceUID, KFPConnectionRef: e.KFPConnectionRef, ExperimentID: e.ExperimentID, Identities: e.Identities}
}
func Digest(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
