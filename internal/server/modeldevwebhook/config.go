// Package modeldevwebhook owns the bounded ModelDev Pod admission endpoint.
// It neither admits executions nor releases retained Pods or workspace claims.
package modeldevwebhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	ConfigSchemaVersion   = "ani.modeldev.webhook.v1"
	ProfileLabel          = "ani.modeldev/admission-profile"
	RoleLabel             = "ani.modeldev/step-role"
	ExitEvidenceFinalizer = "modeldev.ani.io/step-exit-evidence"
)

var ErrConfiguration = errors.New("invalid modeldev webhook configuration")

type LabelBinding struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type NamespaceBinding struct {
	Namespace          string           `json:"namespace"`
	NamespaceUID       string           `json:"namespace_uid"`
	OwnerLabel         LabelBinding     `json:"owner_label"`
	ArgoCreator        string           `json:"argo_creator"`
	StepServiceAccount string           `json:"step_service_account"`
	Profiles           []string         `json:"profiles"`
	Driver             *DriverBinding   `json:"driver,omitempty"`
	Training           *TrainingBinding `json:"training,omitempty"`
}

// TrainingBinding scopes the existing admission endpoint to worker Pods from
// the actual Job controller. It grants no workload-side Kubernetes access.
type TrainingBinding struct {
	ServiceAccount string `json:"service_account"`
	Creator        string `json:"creator"`
}

// DriverBinding freezes the KFP backend executable separately from workload
// profiles. Backend drivers receive exit retention only, not compute mutation.
type DriverBinding struct {
	Image                string   `json:"image"`
	Command              []string `json:"command"`
	AllowedSupportImages []string `json:"allowed_support_images"`
}

// Profile is operator-owned frozen material. A Pod can select only an ID
// already authorized by its namespace binding, and cannot supply policy fields.
type Profile struct {
	ID                      string   `json:"id"`
	Kind                    string   `json:"kind"`
	MaterialFile            string   `json:"material_file"`
	MaterialSHA256          string   `json:"material_sha256"`
	ComponentImage          string   `json:"component_image"`
	AllowedSupportImages    []string `json:"allowed_support_images"`
	RunAsUser               int64    `json:"run_as_user"`
	RunAsGroup              int64    `json:"run_as_group"`
	FSGroup                 int64    `json:"fs_group"`
	EphemeralStorageRequest string   `json:"ephemeral_storage_request"`
	EphemeralStorageLimit   string   `json:"ephemeral_storage_limit"`
	// Configure these only for metadata actually provided by the KFP backend.
	// A configured key is mandatory and must equal the frozen PipelineVersion.
	PipelineVersionLabel      string `json:"pipeline_version_label,omitempty"`
	PipelineVersionAnnotation string `json:"pipeline_version_annotation,omitempty"`
}

type Config struct {
	SchemaVersion                 string             `json:"schema_version"`
	Bindings                      []NamespaceBinding `json:"bindings"`
	Profiles                      []Profile          `json:"profiles"`
	ListenAddress                 string             `json:"listen_address,omitempty"`
	CertificateFile               string             `json:"certificate_file,omitempty"`
	PrivateKeyFile                string             `json:"private_key_file,omitempty"`
	MaxBodyBytes                  int64              `json:"max_body_bytes,omitempty"`
	MaxConcurrentRequests         int                `json:"max_concurrent_requests,omitempty"`
	RequestTimeoutMilliseconds    int                `json:"request_timeout_milliseconds,omitempty"`
	DependencyTimeoutMilliseconds int                `json:"dependency_timeout_milliseconds,omitempty"`
}

type frozenProfile struct {
	Profile
	material cpup01.ImplementationMaterial
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, ErrConfiguration
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Config{}, ErrConfiguration
	}
	if !uniqueJSONKeys(raw) {
		return Config{}, ErrConfiguration
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return Config{}, ErrConfiguration
	}
	if _, err := config.freeze(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config *Config) defaults() {
	if config.ListenAddress == "" {
		config.ListenAddress = ":8443"
	}
	if config.CertificateFile == "" {
		config.CertificateFile = "/tls/tls.crt"
	}
	if config.PrivateKeyFile == "" {
		config.PrivateKeyFile = "/tls/tls.key"
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = 1 << 20
	}
	if config.MaxConcurrentRequests == 0 {
		config.MaxConcurrentRequests = 32
	}
	if config.RequestTimeoutMilliseconds == 0 {
		config.RequestTimeoutMilliseconds = 5000
	}
	if config.DependencyTimeoutMilliseconds == 0 {
		config.DependencyTimeoutMilliseconds = 2000
	}
}

func (config Config) Validate() error { _, err := config.freeze(); return err }

func (config Config) freeze() (map[string]frozenProfile, error) {
	config.defaults()
	if config.SchemaVersion != ConfigSchemaVersion || len(config.Bindings) == 0 || len(config.Bindings) > 32 || len(config.Profiles) == 0 || len(config.Profiles) > 64 ||
		config.MaxBodyBytes < 1024 || config.MaxBodyBytes > 1<<20 || config.MaxConcurrentRequests < 1 || config.MaxConcurrentRequests > 128 ||
		config.RequestTimeoutMilliseconds < 100 || config.RequestTimeoutMilliseconds > 8000 || config.DependencyTimeoutMilliseconds < 50 || config.DependencyTimeoutMilliseconds > 3000 || config.DependencyTimeoutMilliseconds >= config.RequestTimeoutMilliseconds {
		return nil, ErrConfiguration
	}
	frozen := make(map[string]frozenProfile, len(config.Profiles))
	for _, profile := range config.Profiles {
		if len(validation.IsDNS1123Label(profile.ID)) != 0 || (profile.Kind != "GENERAL_TRAINING" && profile.Kind != "FINETUNING") || !filepath.IsAbs(profile.MaterialFile) || !sha256Pattern.MatchString(profile.MaterialSHA256) || !imagePattern.MatchString(profile.ComponentImage) || len(profile.AllowedSupportImages) == 0 || len(profile.AllowedSupportImages) > 8 ||
			profile.RunAsUser != 10001 || profile.RunAsGroup != 10001 || profile.FSGroup != 10001 {
			return nil, ErrConfiguration
		}
		if _, exists := frozen[profile.ID]; exists {
			return nil, ErrConfiguration
		}
		seen := map[string]bool{}
		for _, image := range profile.AllowedSupportImages {
			if !imagePattern.MatchString(image) || seen[image] {
				return nil, ErrConfiguration
			}
			seen[image] = true
		}
		request, err := resource.ParseQuantity(profile.EphemeralStorageRequest)
		limit, limitErr := resource.ParseQuantity(profile.EphemeralStorageLimit)
		minimum, maximum := resource.MustParse("1Gi"), resource.MustParse("12Gi")
		if err != nil || limitErr != nil || request.Cmp(minimum) < 0 || limit.Cmp(request) < 0 || limit.Cmp(maximum) > 0 {
			return nil, ErrConfiguration
		}
		if profile.Kind == "FINETUNING" && (request.Cmp(resource.MustParse("10Gi")) != 0 || limit.Cmp(resource.MustParse("12Gi")) != 0) {
			return nil, ErrConfiguration
		}
		for _, key := range []string{profile.PipelineVersionLabel, profile.PipelineVersionAnnotation} {
			if key != "" && len(validation.IsQualifiedName(key)) != 0 {
				return nil, ErrConfiguration
			}
		}
		file, err := os.Open(profile.MaterialFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, cpup01.MaxMaterialBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, ErrConfiguration
		}
		material, digest, parseErr := cpup01.ParseMaterial(raw)
		if parseErr != nil || digest != profile.MaterialSHA256 || cpup01.Implementations()[material.Implementation.ImplementationID].Kind != profile.Kind {
			return nil, ErrConfiguration
		}
		if profile.Kind == "FINETUNING" && (material.Finetuning == nil || material.Finetuning.ExportProgram.ImageDigest != profile.ComponentImage || material.Finetuning.VerifyProgram.ImageDigest != profile.ComponentImage) {
			return nil, ErrConfiguration
		}
		profile.AllowedSupportImages = append([]string(nil), profile.AllowedSupportImages...)
		frozen[profile.ID] = frozenProfile{Profile: profile, material: material}
	}
	seen := map[string]bool{}
	for _, binding := range config.Bindings {
		if len(validation.IsDNS1123Label(binding.Namespace)) != 0 || seen[binding.Namespace] || !canonicalUUID(binding.NamespaceUID) || len(validation.IsQualifiedName(binding.OwnerLabel.Key)) != 0 || len(validation.IsValidLabelValue(binding.OwnerLabel.Value)) != 0 || binding.OwnerLabel.Value == "" || len(validation.IsDNS1123Subdomain(binding.StepServiceAccount)) != 0 || !creatorPattern.MatchString(binding.ArgoCreator) || len(binding.Profiles) == 0 || len(binding.Profiles) > 64 {
			return nil, ErrConfiguration
		}
		seen[binding.Namespace] = true
		if binding.Training != nil && (len(validation.IsDNS1123Subdomain(binding.Training.ServiceAccount)) != 0 || binding.Training.ServiceAccount == binding.StepServiceAccount || !creatorPattern.MatchString(binding.Training.Creator)) {
			return nil, ErrConfiguration
		}
		if binding.Driver != nil {
			if !imagePattern.MatchString(binding.Driver.Image) || len(binding.Driver.Command) != 1 || (binding.Driver.Command[0] != "driver" && binding.Driver.Command[0] != "/bin/driver") || len(binding.Driver.AllowedSupportImages) == 0 || len(binding.Driver.AllowedSupportImages) > 8 {
				return nil, ErrConfiguration
			}
			seenImages := map[string]bool{}
			for _, image := range binding.Driver.AllowedSupportImages {
				if !imagePattern.MatchString(image) || seenImages[image] {
					return nil, ErrConfiguration
				}
				seenImages[image] = true
			}
			for _, id := range binding.Profiles {
				if frozen[id].ComponentImage == binding.Driver.Image {
					return nil, ErrConfiguration
				}
			}
		}
		ids := map[string]bool{}
		for _, id := range binding.Profiles {
			if _, ok := frozen[id]; !ok || ids[id] {
				return nil, ErrConfiguration
			}
			ids[id] = true
		}
	}
	return frozen, nil
}

var (
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imagePattern   = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
	creatorPattern = regexp.MustCompile(`^system:serviceaccount:[a-z0-9][a-z0-9.-]*:[a-z0-9][a-z0-9.-]*$`)
)

func canonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == strings.ToLower(value) && value == strings.ToLower(value)
}

// The standard decoder rejects unknown typed fields but otherwise accepts
// duplicate keys. Frozen operator policy must never depend on last-key wins.
func uniqueJSONKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
