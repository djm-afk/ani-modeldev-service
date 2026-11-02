package cpup01

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// DispatchOwnerSnapshot is immutable execution authority, covered by SpecHash.
type DispatchOwnerSnapshot struct {
	Reference      string `json:"reference"`
	RevisionSHA256 string `json:"revision_sha256"`
	PipelineRoot   string `json:"pipeline_root"`
}

type ManagedImagePullBinding struct {
	RegistryAuthority string `json:"registry_authority"`
	SecretName        string `json:"secret_name"`
}
type ManagedImageAccessSnapshot struct {
	PullBindings []ManagedImagePullBinding `json:"pull_bindings"`
}

// CanonicalTenantID accepts only the complete canonical nonzero UUID form.
func CanonicalTenantID(value string) bool { return validUUID(value) && strings.ToLower(value) == value }

func (owner DispatchOwnerSnapshot) ValidateFor(tenantID, executionID string) error {
	root, err := url.Parse(owner.PipelineRoot)
	if !CanonicalTenantID(tenantID) || !CanonicalTenantID(executionID) || !validSnapshotReference(owner.Reference) || !snapshotSHA256Pattern.MatchString(owner.RevisionSHA256) || err != nil || root.Scheme != "s3" || !validSnapshotDNSName(root.Host) || root.User != nil || root.Port() != "" || root.RawQuery != "" || root.ForceQuery || root.Fragment != "" || root.RawPath != "" || root.Path != "/artifacts/"+tenantID+"/"+executionID {
		return fmt.Errorf("%w: dispatch owner", ErrInvalidArgument)
	}
	return nil
}
func (access ManagedImageAccessSnapshot) Canonical() ManagedImageAccessSnapshot {
	access.PullBindings = append([]ManagedImagePullBinding{}, access.PullBindings...)
	sort.Slice(access.PullBindings, func(i, j int) bool {
		return access.PullBindings[i].RegistryAuthority < access.PullBindings[j].RegistryAuthority
	})
	return access
}
func (access ManagedImageAccessSnapshot) Validate() error {
	// This release supports one managed registry authority. No implicit empty or
	// per-registry fallback may silently widen the approved image authority.
	if len(access.PullBindings) != 1 {
		return fmt.Errorf("%w: managed image access", ErrInvalidArgument)
	}
	for _, binding := range access.PullBindings {
		registry, err := url.Parse("https://" + binding.RegistryAuthority)
		if err != nil || registry.Host != binding.RegistryAuthority || registry.User != nil || registry.Path != "" || registry.RawQuery != "" || registry.Fragment != "" || !validSnapshotDNSName(registry.Hostname()) {
			return fmt.Errorf("%w: managed registry authority", ErrInvalidArgument)
		}
		if port := registry.Port(); port != "" {
			number, err := strconv.ParseUint(port, 10, 16)
			if err != nil || number == 0 {
				return fmt.Errorf("%w: managed registry port", ErrInvalidArgument)
			}
		}
		if binding.RegistryAuthority == "" || NormalizeRegistryAuthority(binding.RegistryAuthority) != binding.RegistryAuthority || !validPullSecretName(binding.SecretName) {
			return fmt.Errorf("%w: managed image pull binding", ErrInvalidArgument)
		}
	}
	return nil
}

// TenantStorageScope derives the only permitted tenant prefix from platform
// connection and bucket configuration. No environment registration is needed.
func TenantStorageScope(platform StorageScope, tenantID, purpose string) (StorageScope, error) {
	if !CanonicalTenantID(tenantID) {
		return StorageScope{}, ErrInvalidArgument
	}
	switch purpose {
	case "input":
		platform.ApprovedPrefix = "inputs/" + tenantID
	case "publish":
		platform.ApprovedPrefix = "executions/" + tenantID
	case "base-model":
		platform.ApprovedPrefix = tenantID
	default:
		return StorageScope{}, ErrInvalidArgument
	}
	if platform.Validate() != nil {
		return StorageScope{}, ErrInvalidArgument
	}
	return platform, nil
}
