package cpup01

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// ComponentResources freezes only pipeline component Pod resources. Trainer
// resources remain in the execution's ComputeOrigin and formal GPU plan.
type ComponentResources struct {
	CPURequest              string `json:"cpu_request"`
	CPULimit                string `json:"cpu_limit"`
	MemoryRequest           string `json:"memory_request"`
	MemoryLimit             string `json:"memory_limit"`
	EphemeralStorageRequest string `json:"ephemeral_storage_request"`
	EphemeralStorageLimit   string `json:"ephemeral_storage_limit"`
}

// ComponentRoles is the finite set of actual component Pods in the current
// pipeline. CreatePVC is an official resource primitive, not a component Pod.
func ComponentRoles(kind string) []string {
	roles := []string{"workspace-name", "prepare", "train-wait", "collect", "publish", "close", "finalize-close"}
	switch kind {
	case "GENERAL_TRAINING":
		return roles
	case "FINETUNING":
		return append(roles, "export", "verify", "register")
	default:
		return nil
	}
}

func (r ComponentResources) Validate() error {
	for _, pair := range [][2]string{{r.CPURequest, r.CPULimit}, {r.MemoryRequest, r.MemoryLimit}, {r.EphemeralStorageRequest, r.EphemeralStorageLimit}} {
		request, requestErr := resource.ParseQuantity(pair[0])
		limit, limitErr := resource.ParseQuantity(pair[1])
		if len(pair[0]) > MaxRelativePathBytes || len(pair[1]) > MaxRelativePathBytes || requestErr != nil || limitErr != nil || request.Sign() <= 0 || limit.Sign() <= 0 || request.Cmp(limit) > 0 {
			return fmt.Errorf("%w: component resources", ErrInvalidArgument)
		}
	}
	return nil
}

func (r ComponentResources) Canonical() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return encodeCanonicalJSON(r)
}

func (r ComponentResources) Digest() (string, error) { return contractDigest(r.Canonical()) }

func ValidateComponentResources(kind string, values map[string]ComponentResources) error {
	roles := ComponentRoles(kind)
	if len(roles) == 0 || len(roles) != len(values) {
		return fmt.Errorf("%w: component resource roles", ErrInvalidArgument)
	}
	for _, role := range roles {
		value, ok := values[role]
		if !ok || value.Validate() != nil {
			return fmt.Errorf("%w: component resources for %s", ErrInvalidArgument, role)
		}
	}
	return nil
}
