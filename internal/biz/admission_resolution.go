package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrNoCompatibleRelease          = errors.New("NO_COMPATIBLE_RELEASE")
	ErrAdmissionInputNotReady       = errors.New("INPUT_NOT_READY")
	ErrAdmissionEnvironmentNotReady = errors.New("ENVIRONMENT_NOT_READY")
)

// AdmissionReleaseSelection is one explicit Governance pointer observation.
// ModelDev must preserve its generation and never choose a current Release.
type AdmissionReleaseSelection struct {
	ReleaseID         string
	ReleaseDigest     string
	BindingGeneration uint64
}

// AdmissionResolutionRequest follows current authorization and an original-key
// miss in Governance. AcceptedAt is fixed by Governance, not this resolver.
type AdmissionResolutionRequest struct {
	TenantID   string
	Intent     cpup01.Intent
	Release    AdmissionReleaseSelection
	AcceptedAt time.Time
}

// TenantAdmissionFacts is explicit managed configuration, never user JSON.
// The supplying owner must establish the tenant association, revision digest,
// actual identities, storage authority and Runtime/workspace mount evidence.
// A typed value or a matching shape alone establishes none of those facts.
type TenantAdmissionFacts struct {
	TenantID            string
	Environment         cpup01.EnvironmentBindingSnapshot
	PublicationScope    cpup01.StorageScope
	InputScope          cpup01.StorageScope
	Runtime             cpup01.RuntimeRef
	Workspace           cpup01.WorkspaceContract
	InputFilePath       string
	OutputDirectoryPath string
}

// AdmissionResolution is an unpersisted candidate, not an acceptance receipt.
// Governance must bind real command IDs and commit its generation/key checks.
type AdmissionResolution struct {
	Snapshot cpup01.Snapshot
	SpecHash string
}

// AdmissionReleaseReader returns a validated canonical Release whose actual
// bytes match the selected ID and digest, or a finite domain error.
type AdmissionReleaseReader interface {
	ReadRelease(context.Context, string, string) (cpup01.ReleaseDocument, error)
}

type AdmissionInputReader interface {
	Get(context.Context, string, string) (InputVersion, error)
}

// AdmissionFactsReader returns managed facts for one explicit tenant/Release
// combination. Governance's current binding generation never selects this data.
// The reader fixes actual configured bytes; it does not certify ENV readiness.
type AdmissionFactsReader interface {
	ReadAdmissionFacts(context.Context, string, string, string) (TenantAdmissionFacts, error)
}

// AdmissionResolver owns fixed-fact composition, not authentication, current
// pointers, environment discovery, command identity allocation or persistence.
type AdmissionResolver struct {
	releases AdmissionReleaseReader
	inputs   AdmissionInputReader
	facts    AdmissionFactsReader
	models   FinetuningModelFactsReader
}

func NewAdmissionResolver(releases AdmissionReleaseReader, inputs AdmissionInputReader) *AdmissionResolver {
	return &AdmissionResolver{releases: releases, inputs: inputs}
}

// NewManagedAdmissionResolver binds a real managed facts owner. Callers of its
// managed entry point cannot supply environment or storage facts in a request.
func NewManagedAdmissionResolver(releases AdmissionReleaseReader, inputs AdmissionInputReader, facts AdmissionFactsReader) *AdmissionResolver {
	return &AdmissionResolver{releases: releases, inputs: inputs, facts: facts}
}

func (resolver *AdmissionResolver) ResolveManaged(ctx context.Context, request AdmissionResolutionRequest) (AdmissionResolution, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	if resolver == nil || resolver.facts == nil {
		return AdmissionResolution{}, ErrAdmissionEnvironmentNotReady
	}
	facts, err := resolver.facts.ReadAdmissionFacts(ctx, request.TenantID, request.Release.ReleaseID, request.Release.ReleaseDigest)
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	if err != nil {
		return AdmissionResolution{}, err
	}
	return resolver.Resolve(ctx, request, facts)
}

func (resolver *AdmissionResolver) Resolve(ctx context.Context, request AdmissionResolutionRequest, facts TenantAdmissionFacts) (AdmissionResolution, error) {
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	intent, err := canonicalResolutionIntent(request.Intent)
	if err != nil {
		return AdmissionResolution{}, err
	}
	acceptedAt := request.AcceptedAt.UTC()
	if !validAdmissionID(request.TenantID) || !validAdmissionID(request.Release.ReleaseID) ||
		!closeSpecHashPattern.MatchString(request.Release.ReleaseDigest) || request.Release.BindingGeneration == 0 ||
		acceptedAt.IsZero() || acceptedAt.Year() < 1 || acceptedAt.Year() > 9999 || acceptedAt.Nanosecond()%1000 != 0 {
		return AdmissionResolution{}, ErrInvalidAdmission
	}
	if !strings.EqualFold(facts.TenantID, request.TenantID) {
		return AdmissionResolution{}, ErrAdmissionEnvironmentNotReady
	}
	if resolver == nil || resolver.releases == nil || resolver.inputs == nil {
		return AdmissionResolution{}, ErrPersistence
	}
	release, err := resolver.releases.ReadRelease(ctx, strings.ToLower(request.Release.ReleaseID), request.Release.ReleaseDigest)
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	if err != nil {
		return AdmissionResolution{}, err
	}
	if release.ReleaseID != strings.ToLower(request.Release.ReleaseID) || release.PresetID != intent.PresetID ||
		release.Kind != intent.Kind || !supportedAdmissionProgram(release) ||
		release.ExecutionTimeoutSeconds == 0 ||
		(intent.ImageVersionID != nil && *intent.ImageVersionID != release.Program.ImageVersionID) {
		return AdmissionResolution{}, ErrNoCompatibleRelease
	}
	version, err := resolver.inputs.Get(ctx, strings.ToLower(request.TenantID), intent.DatasetVersionID)
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	if err != nil {
		return AdmissionResolution{}, err
	}
	if !strings.EqualFold(version.Import.TenantID, request.TenantID) || version.Import.InputVersionID != intent.DatasetVersionID {
		return AdmissionResolution{}, ErrPersistence
	}
	input, err := verifiedAdmissionInput(version, intent.Kind)
	if err != nil {
		return AdmissionResolution{}, err
	}
	if !compatibleAdmissionFacts(facts, release, version.Import.Object) {
		return AdmissionResolution{}, ErrAdmissionEnvironmentNotReady
	}
	// Parsed Release defaults and canonical explicit parameters share the same
	// registered grammar. Merge copies, preserving the original Intent presence.
	parameters := slices.Clone(release.Program.DefaultParameters)
	var finetuning *cpup01.FinetuningSnapshot
	if intent.Kind == "FINETUNING" {
		if resolver.models == nil {
			return AdmissionResolution{}, ErrFinetuningNotEnabled
		}
		parameters, err = cpup01.ResolveFinetuningParameters(release.Program, *intent.Finetuning)
		if err != nil {
			return AdmissionResolution{}, err
		}
		finetuning, err = resolver.resolveFinetuningModelFacts(ctx, request.TenantID, intent, release, parameters, input.Object)
		if err != nil {
			return AdmissionResolution{}, err
		}
	}
	if intent.GeneralParameters != nil {
		for _, selected := range *intent.GeneralParameters {
			matched := false
			for index := range parameters {
				if parameters[index].Name == selected.Name {
					parameters[index] = selected
					matched = true
					break
				}
			}
			if !matched {
				return AdmissionResolution{}, ErrNoCompatibleRelease
			}
		}
	}
	var arguments []string
	if finetuning != nil {
		arguments, err = resolveFinetuningArguments(release.Program.ArgsTemplate, input.Object, finetuning.BaseModel)
	} else {
		arguments, err = resolveAdmissionArguments(release.Program.ArgsTemplate, parameters, facts, version.Import.Object)
	}
	if err != nil {
		return AdmissionResolution{}, err
	}
	deadline := acceptedAt.Add(time.Duration(release.ExecutionTimeoutSeconds) * time.Second)
	if !deadline.After(acceptedAt) || deadline.Year() > 9999 {
		return AdmissionResolution{}, ErrInvalidAdmission
	}
	snapshot := cpup01.Snapshot{
		SchemaVersion: cpup01.SnapshotSchemaVersion, Kind: release.Kind, DeliveryMode: release.DeliveryMode,
		Release: cpup01.ReleaseSnapshot{
			ReleaseID: release.ReleaseID, ReleaseDigest: request.Release.ReleaseDigest, PresetID: release.PresetID,
			AcceptedBindingGeneration: request.Release.BindingGeneration,
			PipelineID:                release.PipelineID, PipelineVersionID: release.PipelineVersionID,
			PipelineIRSHA256: release.PipelineIRSHA256, Runtime: release.Runtime,
		},
		Input: input,
		Program: cpup01.ProgramRef{
			ImageVersionID: release.Program.ImageVersionID, ImageDigest: release.Program.ImageDigest,
			Command: release.Program.Command, ResolvedArgs: arguments, ResolvedParameters: parameters,
		},
		Resources: release.Resources, Environment: facts.Environment, Workspace: release.Workspace,
		PublicationScope: facts.PublicationScope, OutputContract: release.OutputContract, DeadlineAt: deadline,
		Finetuning: finetuning,
	}
	canonical, err := snapshot.Canonical()
	if err != nil {
		return AdmissionResolution{}, ErrInvalidAdmission
	}
	// Decode only our validated canonical bytes to return a fully independent,
	// normalized typed value. No caller-owned slice or optional pointer escapes.
	var normalized cpup01.Snapshot
	if json.Unmarshal(canonical, &normalized) != nil {
		return AdmissionResolution{}, ErrInvalidAdmission
	}
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	digest := sha256.Sum256(canonical)
	return AdmissionResolution{Snapshot: normalized, SpecHash: hex.EncodeToString(digest[:])}, nil
}

func canonicalResolutionIntent(original cpup01.Intent) (cpup01.Intent, error) {
	canonical, _, err := cpup01.CanonicalIntent(original)
	if err != nil {
		return cpup01.Intent{}, ErrInvalidAdmission
	}
	var value struct {
		Schema string `json:"schema"`
		cpup01.Intent
	}
	if json.Unmarshal(canonical, &value) != nil {
		return cpup01.Intent{}, ErrInvalidAdmission
	}
	return value.Intent, nil
}

func compatibleAdmissionFacts(facts TenantAdmissionFacts, release cpup01.ReleaseDocument, object cpup01.FixedObjectRef) bool {
	want, actual := release.Runtime, facts.Runtime
	wantTargets, actualTargets := slices.Clone(want.TargetJobs), slices.Clone(actual.TargetJobs)
	slices.Sort(wantTargets)
	slices.Sort(actualTargets)
	if want.Name != actual.Name || want.Kind != actual.Kind || want.APIGroup != actual.APIGroup ||
		want.ContentSHA256 != actual.ContentSHA256 || !slices.Equal(wantTargets, actualTargets) || release.Workspace != facts.Workspace {
		return false
	}
	scope := facts.InputScope
	if scope.StorageConnectionID != object.StorageConnectionID || scope.Bucket != object.Bucket ||
		!ValidStorageKey(scope.ApprovedPrefix) || !strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		return false
	}
	input, output := facts.InputFilePath, facts.OutputDirectoryPath
	return validAdmissionContainerPath(input) && validAdmissionContainerPath(output) &&
		input != output && !strings.HasPrefix(input, output+"/") && !strings.HasPrefix(output, input+"/")
}

func validAdmissionContainerPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value &&
		ValidStorageKey(strings.TrimPrefix(value, "/")) && !strings.ContainsAny(value, "*?[]")
}

func resolveAdmissionArguments(template []cpup01.ReleaseArgument, parameters []cpup01.Parameter, facts TenantAdmissionFacts, object cpup01.FixedObjectRef) ([]string, error) {
	learningRate := ""
	for _, parameter := range parameters {
		if parameter.Name == "learning_rate" {
			learningRate = parameter.Value
		}
	}
	values := map[string]string{
		"INPUT_PATH": facts.InputFilePath, "OUTPUT_PATH": facts.OutputDirectoryPath,
		"INPUT_SHA256": object.SHA256, "INPUT_BYTES": strconv.FormatInt(object.SizeBytes, 10), "LEARNING_RATE": learningRate,
	}
	arguments := make([]string, 0, len(template))
	for _, argument := range template {
		if (argument.Literal == "") == (argument.Source == "") {
			return nil, ErrNoCompatibleRelease
		}
		value := argument.Literal
		if argument.Source != "" {
			var found bool
			value, found = values[argument.Source]
			if !found || value == "" {
				return nil, ErrNoCompatibleRelease
			}
		}
		arguments = append(arguments, value)
	}
	return arguments, nil
}
