package tenantenvironment

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/client-go/dynamic"
)

// Config is a single platform configuration, not a list of tenant bindings.
// Its canonical bytes are frozen at first registration. File references carry
// no credentials; the token and artifact values are read afresh for each use.
type Config struct {
	SchemaVersion                         string                       `json:"schema_version"`
	ClusterID                             string                       `json:"cluster_id"`
	KFPConnectionRef                      string                       `json:"kfp_connection_ref"`
	KFPEndpoint                           string                       `json:"kfp_endpoint"`
	KFPTokenFile                          string                       `json:"kfp_token_file"`
	KFPCAFile                             string                       `json:"kfp_ca_file"`
	ExperimentUniquenessVerified          bool                         `json:"experiment_uniqueness_verified"`
	ExperimentUniquenessEvidenceSHA256    string                       `json:"experiment_uniqueness_evidence_sha256"`
	ArtifactEndpoint                      string                       `json:"artifact_endpoint"`
	ArtifactRegion                        string                       `json:"artifact_region"`
	ArtifactBucket                        string                       `json:"artifact_bucket"`
	ArtifactAccessKeyFile                 string                       `json:"artifact_access_key_file"`
	ArtifactSecretKeyFile                 string                       `json:"artifact_secret_key_file"`
	ArtifactBucketIsolationEvidenceSHA256 string                       `json:"artifact_bucket_isolation_evidence_sha256"`
	KFPCAKey                              string                       `json:"kfp_ca_key"`
	KFPPublicCA                           string                       `json:"kfp_public_ca"`
	MetadataHost                          string                       `json:"metadata_host"`
	MetadataPort                          string                       `json:"metadata_port"`
	StepOwner                             StepOwnerConfig              `json:"step_owner"`
	ModeldevPublicCA                      string                       `json:"modeldev_public_ca"`
	S3PublicCA                            string                       `json:"s3_public_ca"`
	RegistryAuthority                     string                       `json:"registry_authority"`
	PlatformProject                       string                       `json:"platform_project"`
	InputScope                            cpup01.StorageScope          `json:"input_scope"`
	PublicationScope                      cpup01.StorageScope          `json:"publication_scope"`
	ModelScope                            cpup01.StorageScope          `json:"model_scope"`
	Workspace                             cpup01.WorkspaceContract     `json:"workspace"`
	Timing                                cpup01.TimingConfiguration   `json:"timing"`
	InputFilePath                         string                       `json:"input_file_path"`
	OutputDirectoryPath                   string                       `json:"output_directory_path"`
	TargetPlatform                        cpup01.TrainingPlatform      `json:"target_platform"`
	DispatchOwner                         cpup01.DispatchOwnerSnapshot `json:"dispatch_owner"`
	MaterialBundleDigest                  string                       `json:"material_bundle_digest"`
	Profiles                              []projection.Profile         `json:"profiles"`
	ControllerPrincipals                  []string                     `json:"controller_principals"`
	KFPAPIServiceAccount                  Subject                      `json:"kfp_api_service_account"`
	Bootstrap                             []BootstrapObject            `json:"bootstrap"`
	AnonymousImages                       []AnonymousImage             `json:"anonymous_images"`
	IRSources                             []IRSource                   `json:"ir_sources"`
}
type AnonymousImage struct {
	Reference      string `json:"reference"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}
type IRSource struct {
	ProfileID string `json:"profile_id"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
}
type Subject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}
type BootstrapObject struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	UID    string `json:"uid"`
	SHA256 string `json:"sha256"`
}
type StepOwnerConfig struct {
	ContextFile         string             `json:"context_file"`
	TokenFile           string             `json:"token_file"`
	Target              string             `json:"grpc_target"`
	ServerName          string             `json:"grpc_server_name"`
	CAFile              string             `json:"grpc_ca_file"`
	WorkspaceDirectory  string             `json:"workspace_directory"`
	PVCName             string             `json:"pvc_name"`
	InventoryFile       string             `json:"inventory_file"`
	PublicationRefFile  string             `json:"publication_ref_file"`
	PublicationRef      string             `json:"publication_ref"`
	PollIntervalSeconds int                `json:"poll_interval_seconds"`
	TimeoutSeconds      int                `json:"timeout_seconds"`
	S3                  *StepStorageConfig `json:"s3,omitempty"`
}
type StepStorageConfig struct {
	Endpoint                string `json:"endpoint"`
	Region                  string `json:"region"`
	CAFile                  string `json:"ca_file"`
	CredentialsFile         string `json:"credentials_file"`
	StreamingTimeoutSeconds int    `json:"streaming_timeout_seconds,omitempty"`
}

func LoadConfig(file, digest string) (Config, error) {
	var c Config
	raw, err := readRegularFile(file, 1<<20)
	if err != nil || biz.EnvironmentDigest(raw) != digest {
		return c, biz.ErrTenantEnvironmentInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, biz.ErrTenantEnvironmentInvalid
	}
	if err = c.validate(); err != nil {
		return c, err
	}
	return c, nil
}
func readFile(path string, max int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	v, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(v)) > max {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	return v, nil
}
func readRegularFile(path string, max int64) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > max {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(raw)) > max {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	return raw, nil
}
func validDigest(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
func (c Config) validate() error {
	if c.SchemaVersion != "ani.modeldev.tenant-environment-platform.v1" || !biz.CanonicalEnvironmentUUID(c.ClusterID) || c.KFPConnectionRef == "" || !strings.HasPrefix(c.KFPEndpoint, "https://") || !strings.HasPrefix(c.ArtifactEndpoint, "https://") || c.KFPTokenFile == "" || c.KFPCAFile == "" || c.ArtifactAccessKeyFile == "" || c.ArtifactSecretKeyFile == "" || c.ArtifactBucket == "" || c.ArtifactRegion == "" || c.KFPCAKey == "" || c.KFPPublicCA == "" || c.MetadataHost == "" || c.MetadataPort == "" || c.RegistryAuthority == "" || cpup01.NormalizeRegistryAuthority(c.RegistryAuthority) != c.RegistryAuthority || c.PlatformProject == "" || strings.Contains(c.PlatformProject, "/") || !validDigest(c.MaterialBundleDigest) || len(c.Profiles) != 4 || len(c.ControllerPrincipals) == 0 {
		return biz.ErrTenantEnvironmentInvalid
	}
	if !validDigest(c.ArtifactBucketIsolationEvidenceSHA256) || len(c.Bootstrap) != 12 {
		return biz.EnvironmentError("ENVIRONMENT_AUTHORIZATION_UNVERIFIED")
	}
	if c.ExperimentUniquenessVerified && !validDigest(c.ExperimentUniquenessEvidenceSHA256) {
		return biz.ErrTenantEnvironmentInvalid
	}
	wanted := map[string]bool{"modeldev-mlp-cpu": true, "modeldev-mlp-gpu": true, "modeldev-sft-lora-cpu": true, "modeldev-sft-lora-gpu": true}
	for _, p := range c.Profiles {
		if !wanted[p.ID] || !biz.CanonicalEnvironmentUUID(p.PipelineID) || !biz.CanonicalEnvironmentUUID(p.VersionID) || !validDigest(p.IRSHA256) || !validDigest(p.MaterialSHA256) {
			return biz.ErrTenantEnvironmentInvalid
		}
		delete(wanted, p.ID)
	}
	if len(c.IRSources) != 4 {
		return biz.ErrTenantEnvironmentInvalid
	}
	seenIR := map[string]bool{}
	for _, source := range c.IRSources {
		match := false
		for _, profile := range c.Profiles {
			if source.ProfileID == profile.ID && source.SHA256 == profile.IRSHA256 {
				match = true
			}
		}
		if !match || source.File == "" || seenIR[source.ProfileID] {
			return biz.ErrTenantEnvironmentInvalid
		}
		seenIR[source.ProfileID] = true
	}
	if c.KFPAPIServiceAccount.Kind != "ServiceAccount" || c.KFPAPIServiceAccount.Name == "" || c.KFPAPIServiceAccount.Namespace == "" {
		return biz.ErrTenantEnvironmentInvalid
	}
	return nil
}

type Platform struct {
	config Config
	kube   dynamic.Interface
	images biz.TrainingImageEnsurer
	kfp    *experimentClient
}

func NewPlatform(config Config, kube dynamic.Interface, images biz.TrainingImageEnsurer) (*Platform, error) {
	if e := config.validate(); e != nil {
		return nil, e
	}
	if kube == nil || images == nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	raw, e := readFile(config.KFPCAFile, 1<<20)
	if e != nil {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, biz.ErrTenantEnvironmentInvalid
	}
	kfp, e := newExperimentClient(config, roots)
	if e != nil {
		return nil, e
	}
	sort.Slice(config.Profiles, func(i, j int) bool { return config.Profiles[i].ID < config.Profiles[j].ID })
	sort.Strings(config.ControllerPrincipals)
	return &Platform{config: config, kube: kube, images: images, kfp: kfp}, nil
}
func (p *Platform) ProvisionSpec() ([]byte, error) { return json.Marshal(p.config) }
