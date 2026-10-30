package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// TrainingImageProtocolVersion names the training image compatibility contract
// this repository implements. It is frozen into the selection so a future
// protocol change cannot silently reuse an older accepted execution.
const TrainingImageProtocolVersion = "ani.modeldev.training-image-protocol.v1"

// ErrIncompletePullScope reports a pull Secret scope that cannot be derived.
var ErrIncompletePullScope = errors.New("image pull scope requires tenant, cluster, namespace and registry")

// TrainingPlatform is the resolved target platform of the selected image. It
// comes from the pinned admission facts, never from the CPU/GPU device name or
// the image name.
type TrainingPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

// Validate reports whether the pinned platform is a usable OCI platform tuple.
func (platform TrainingPlatform) Validate() error {
	if !validSnapshotDNSLabel(platform.OS) || !validSnapshotDNSLabel(platform.Architecture) || (platform.Variant != "" && !validSnapshotDNSLabel(platform.Variant)) {
		return fmt.Errorf("%w: training image platform", ErrInvalidArgument)
	}
	return nil
}

// TrainingImageFacts is the frozen catalogue resolution of the training image,
// beyond the identity already carried by ProgramRef. It never contains
// credentials: only the non-sensitive Secret name is retained, and the
// plaintext material stays in Kubernetes.
type TrainingImageFacts struct {
	// RegistryAuthority is Resource's authoritative value. Consumers must never
	// re-derive it from the resolved reference.
	RegistryAuthority string `json:"registry_authority"`
	// Platform is the resolved target platform the image was selected for.
	Platform TrainingPlatform `json:"platform"`
	// ProtocolVersion is the compatibility contract the image was validated for.
	ProtocolVersion string `json:"protocol_version"`
	// PullSecretRef is the shared tenant pull Secret name derived offline from
	// the same rule Resource uses. It grants nothing by itself.
	PullSecretRef string `json:"pull_secret_ref"`
}

// PullSecretName mirrors Resource's single naming rule. The shared Secret is
// scoped by tenant, cluster, namespace and registry authority; the workload
// kind never participates in its name. cluster_id must stay in the hash so the
// same tenant namespace name in two clusters cannot collide.
func PullSecretName(tenantID, clusterID, namespace, registry string) (string, error) {
	parts := []string{strings.ToLower(tenantID), strings.ToLower(clusterID), strings.ToLower(namespace), NormalizeRegistryAuthority(registry)}
	for _, part := range parts {
		if part == "" {
			return "", ErrIncompletePullScope
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "ani-pull-" + hex.EncodeToString(sum[:16]), nil
}

// NormalizeRegistryAuthority lowercases the authority and drops any scheme,
// path and the default registry port. It is the single normalization used by
// every writer and consumer of the shared Secret name.
func NormalizeRegistryAuthority(authority string) string {
	a := strings.TrimSpace(strings.ToLower(authority))
	if i := strings.Index(a, "://"); i >= 0 {
		a = a[i+3:]
	}
	if i := strings.IndexAny(a, "/?#"); i >= 0 {
		a = a[:i]
	}
	if strings.HasSuffix(a, ":443") {
		a = strings.TrimSuffix(a, ":443")
	}
	return a
}

// ValidTrainingImageID reports whether the value is a catalogue image identity
// rather than a registry reference.
func ValidTrainingImageID(value string) bool { return registeredImageIDPattern.MatchString(value) }

func (facts TrainingImageFacts) canonical() TrainingImageFacts {
	facts.RegistryAuthority = NormalizeRegistryAuthority(facts.RegistryAuthority)
	facts.Platform.OS = strings.ToLower(facts.Platform.OS)
	facts.Platform.Architecture = strings.ToLower(facts.Platform.Architecture)
	facts.Platform.Variant = strings.ToLower(facts.Platform.Variant)
	facts.ProtocolVersion = strings.ToLower(facts.ProtocolVersion)
	return facts
}

// validate cross-checks the resolution facts against the executable program so
// a snapshot can never claim one image and run another.
func (facts TrainingImageFacts) validate(program ProgramRef) error {
	if facts.RegistryAuthority == "" || NormalizeRegistryAuthority(facts.RegistryAuthority) != facts.RegistryAuthority ||
		facts.ProtocolVersion != TrainingImageProtocolVersion {
		return fmt.Errorf("%w: training image facts", ErrInvalidArgument)
	}
	// The program's image is the resolved runnable reference, so the authority
	// it runs from must be the one Resource reported.
	if !validSnapshotImage(program.ImageDigest) || !strings.HasPrefix(program.ImageDigest, facts.RegistryAuthority+"/") {
		return fmt.Errorf("%w: training image reference", ErrInvalidArgument)
	}
	platform := facts.Platform
	if platform.Validate() != nil {
		return fmt.Errorf("%w: training image platform", ErrInvalidArgument)
	}
	if !validPullSecretName(facts.PullSecretRef) {
		return fmt.Errorf("%w: training pull Secret reference", ErrInvalidArgument)
	}
	return nil
}

// validPullSecretName keeps the shared Secret name check local instead of
// importing the Kubernetes validation package into the pure contract.
func validPullSecretName(name string) bool {
	const prefix = "ani-pull-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	return strings.Trim(strings.TrimPrefix(name, prefix), "0123456789abcdef") == ""
}

// DefaultTrainingImage is the trusted system default selected from the same
// catalogue. It carries no digest: Resource resolves it exactly like a
// user-selected image, so a stale material can never pin an old image.
type DefaultTrainingImage struct {
	ImageID string     `json:"image_id"`
	Scope   ImageScope `json:"scope"`
}

func (image DefaultTrainingImage) validate() error {
	if !validRegisteredImage(image.ImageID, 1, image.Scope) {
		return fmt.Errorf("%w: default training image", ErrInvalidArgument)
	}
	return nil
}
