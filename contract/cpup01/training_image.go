package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// TrainingImageSelection is the frozen catalogue resolution of the training
// image. It never contains credentials: only the non-sensitive Secret name is
// retained, and the plaintext material stays in Kubernetes.
type TrainingImageSelection struct {
	ImageID string `json:"image_id"`
	Scope   string `json:"scope"`
	// RegistryAuthority is Resource's authoritative value. Consumers must never
	// re-derive it from the resolved reference.
	RegistryAuthority string `json:"registry_authority"`
	// Digest is the fixed content digest as `sha256:<64 lowercase hex>`.
	Digest string `json:"digest"`
	// Reference is the complete runnable reference, including @sha256:<digest>.
	Reference string           `json:"reference"`
	Platform  TrainingPlatform `json:"platform"`
	// ProtocolVersion is the compatibility contract the image was validated for.
	ProtocolVersion string `json:"protocol_version"`
	// PullSecretRef is the shared tenant pull Secret name derived offline from
	// the same rule Resource uses. It grants nothing by itself.
	PullSecretRef string `json:"pull_secret_ref"`
}

// TrainingProgram is the training-chain executable program. It is deliberately
// separate from ProgramRef so the finetuning chain keeps its own immutable
// identity and hash.
type TrainingProgram struct {
	Image              TrainingImageSelection `json:"image"`
	Command            []string               `json:"command"`
	ResolvedArgs       []string               `json:"resolved_args"`
	ResolvedParameters []Parameter            `json:"resolved_parameters"`
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
func ValidTrainingImageID(value string) bool { return validTrainingImageID(value) }

func (selection TrainingImageSelection) validate() error {
	if !validTrainingImageID(selection.ImageID) || (selection.Scope != "platform" && selection.Scope != "tenant") ||
		selection.RegistryAuthority == "" || NormalizeRegistryAuthority(selection.RegistryAuthority) != selection.RegistryAuthority ||
		selection.ProtocolVersion != TrainingImageProtocolVersion {
		return fmt.Errorf("%w: training image selection", ErrInvalidArgument)
	}
	if !validSnapshotImage(selection.Reference) || !strings.HasSuffix(selection.Reference, "@"+selection.Digest) || !strings.HasPrefix(selection.Reference, selection.RegistryAuthority+"/") {
		return fmt.Errorf("%w: training image reference", ErrInvalidArgument)
	}
	if !snapshotSHA256Pattern.MatchString(strings.TrimPrefix(selection.Digest, "sha256:")) || !strings.HasPrefix(selection.Digest, "sha256:") {
		return fmt.Errorf("%w: training image digest", ErrInvalidArgument)
	}
	platform := selection.Platform
	if !validSnapshotDNSLabel(platform.OS) || !validSnapshotDNSLabel(platform.Architecture) || (platform.Variant != "" && !validSnapshotDNSLabel(platform.Variant)) {
		return fmt.Errorf("%w: training image platform", ErrInvalidArgument)
	}
	if !validPullSecretName(selection.PullSecretRef) {
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

func (program TrainingProgram) canonical() TrainingProgram {
	program.Image.ImageID = strings.ToLower(program.Image.ImageID)
	program.Image.Scope = strings.ToLower(program.Image.Scope)
	program.Image.ProtocolVersion = strings.ToLower(program.Image.ProtocolVersion)
	program.Image.Platform.OS = strings.ToLower(program.Image.Platform.OS)
	program.Image.Platform.Architecture = strings.ToLower(program.Image.Platform.Architecture)
	program.Image.Platform.Variant = strings.ToLower(program.Image.Platform.Variant)
	program.Command = append([]string{}, program.Command...)
	program.ResolvedArgs = append([]string{}, program.ResolvedArgs...)
	program.ResolvedParameters = canonicalParameters(program.ResolvedParameters)
	return program
}

// RuntimeImage is the complete reference a Pod must run. It is the only
// executable image fact; no caller supplies a bare reference.
func (program TrainingProgram) RuntimeImage() string { return program.Image.Reference }

func (program TrainingProgram) validate(implementationID string) error {
	if program.Image.validate() != nil {
		return fmt.Errorf("%w: training program image", ErrInvalidArgument)
	}
	if len(program.Command) == 0 || len(program.Command) > 32 || len(program.ResolvedArgs) > 256 {
		return fmt.Errorf("%w: training program command", ErrInvalidArgument)
	}
	for _, value := range append(append([]string{}, program.Command...), program.ResolvedArgs...) {
		if !validSnapshotText(value) {
			return fmt.Errorf("%w: training program argument", ErrInvalidArgument)
		}
	}
	if ValidateResolvedParameters(implementationID, program.ResolvedParameters) != nil {
		return fmt.Errorf("%w: training program parameters", ErrInvalidArgument)
	}
	return nil
}

// DefaultTrainingImage is the trusted system default selected from the same
// catalogue. It carries no digest: Resource resolves it exactly like a
// user-selected image, so a stale material can never pin an old image.
type DefaultTrainingImage struct {
	ImageID string `json:"image_id"`
	Scope   string `json:"scope"`
}

func (image DefaultTrainingImage) validate() error {
	if !validTrainingImageID(image.ImageID) || (image.Scope != "platform" && image.Scope != "tenant") {
		return fmt.Errorf("%w: default training image", ErrInvalidArgument)
	}
	return nil
}

// decodeTrainingProgram round-trips through canonical JSON so callers cannot
// retain provider-owned slices.
func decodeTrainingProgram(program TrainingProgram) (TrainingProgram, error) {
	raw, err := json.Marshal(program)
	if err != nil {
		return TrainingProgram{}, ErrInvalidArgument
	}
	var decoded TrainingProgram
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return TrainingProgram{}, ErrInvalidArgument
	}
	return decoded, nil
}
