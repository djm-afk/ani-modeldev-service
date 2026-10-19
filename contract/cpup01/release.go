package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const ReleaseSchemaVersion = "ani.modeldev.release.v1"
const MaxReleaseBytes = 64 * 1024

// ReleaseDocument is a versioned catalogue file, not an accepted execution or
// a current binding. Loading it proves no environment or publication readiness.
// Its digest is external: SHA256 of the complete canonical file bytes.
type ReleaseDocument struct {
	SchemaVersion           string             `json:"schema_version"`
	ReleaseID               string             `json:"release_id"`
	PresetID                string             `json:"preset_id"`
	Kind                    string             `json:"kind"`
	DeliveryMode            string             `json:"delivery_mode"`
	PipelineID              string             `json:"pipeline_id"`
	PipelineVersionID       string             `json:"pipeline_version_id"`
	PipelineIRSHA256        string             `json:"pipeline_ir_sha256"`
	Runtime                 RuntimeRef         `json:"runtime"`
	Program                 ReleaseProgram     `json:"program"`
	Resources               CPUResources       `json:"resources"`
	Workspace               WorkspaceContract  `json:"workspace"`
	OutputContract          OutputContract     `json:"output_contract"`
	ExecutionTimeoutSeconds uint32             `json:"execution_timeout_seconds"`
	Finetuning              *FinetuningRelease `json:"finetuning,omitempty"`
}

// ReleaseProgram is the single registered CPU MLP program in this Release.
// Parameters remain governed by the existing CPU-P01 intent grammar.
type ReleaseProgram struct {
	ImageVersionID           string            `json:"image_version_id"`
	ImageDigest              string            `json:"image_digest"`
	Command                  []string          `json:"command"`
	ArgsTemplate             []ReleaseArgument `json:"args_template"`
	ParameterContractVersion string            `json:"parameter_contract_version"`
	DefaultParameters        []Parameter       `json:"default_parameters"`
	ParameterRules           []ParameterRule   `json:"parameter_rules,omitempty"`
	AllowedParameterSets     [][]Parameter     `json:"allowed_parameter_sets,omitempty"`
}

// ReleaseArgument is exactly one literal or one whole-argument source token.
// The CPU contract has a finite token set; it never performs shell expansion.
type ReleaseArgument struct {
	Literal string `json:"literal,omitempty"`
	Source  string `json:"source,omitempty"`
}

// ParseRelease accepts only canonical v1 JSON and returns its actual byte digest.
// Structural validity does not verify remote facts or authorize enabling it.
func ParseRelease(raw []byte) (ReleaseDocument, string, error) {
	invalid := func(field string) (ReleaseDocument, string, error) {
		return ReleaseDocument{}, "", fmt.Errorf("%w: release %s", ErrInvalidArgument, field)
	}
	if len(raw) == 0 || len(raw) > MaxReleaseBytes || !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return invalid("encoding or size")
	}
	var document ReleaseDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return invalid("JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalid("trailing JSON")
	}
	if field := invalidReleaseDocument(document); field != "" {
		return invalid(field)
	}
	// Exact re-encoding also rejects duplicate keys, nulls, alternate key case,
	// omitted required zero-valued fields and noncanonical integer spellings.
	canonical, err := document.Canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return invalid("noncanonical bytes")
	}
	digest := sha256.Sum256(raw)
	return document, hex.EncodeToString(digest[:]), nil
}

// Canonical validates and orders a new immutable catalogue document. It does
// not publish the document, approve a candidate or mutate caller-owned slices.
func (document ReleaseDocument) Canonical() ([]byte, error) {
	if field := invalidReleaseDocument(document); field != "" {
		return nil, fmt.Errorf("%w: release %s", ErrInvalidArgument, field)
	}
	document.ReleaseID = strings.ToLower(document.ReleaseID)
	document.PresetID = strings.ToLower(document.PresetID)
	document.PipelineID = strings.ToLower(document.PipelineID)
	document.PipelineVersionID = strings.ToLower(document.PipelineVersionID)
	document.Program.ImageVersionID = strings.ToLower(document.Program.ImageVersionID)
	document.Runtime.TargetJobs = append([]string{}, document.Runtime.TargetJobs...)
	sort.Strings(document.Runtime.TargetJobs)
	document.Program.DefaultParameters = canonicalParameters(document.Program.DefaultParameters)
	document.Program.ParameterRules = canonicalParameterRules(document.Program.ParameterRules)
	if document.Program.AllowedParameterSets != nil {
		sets := make([][]Parameter, len(document.Program.AllowedParameterSets))
		for i, set := range document.Program.AllowedParameterSets {
			sets[i] = canonicalParameters(set)
		}
		sort.Slice(sets, func(i, j int) bool {
			a, _ := encodeCanonicalJSON(sets[i])
			b, _ := encodeCanonicalJSON(sets[j])
			return bytes.Compare(a, b) < 0
		})
		document.Program.AllowedParameterSets = sets
	}
	document.OutputContract.RequiredFiles = append([]RequiredOutput{}, document.OutputContract.RequiredFiles...)
	sort.Slice(document.OutputContract.RequiredFiles, func(i, j int) bool {
		return document.OutputContract.RequiredFiles[i].RelativePath < document.OutputContract.RequiredFiles[j].RelativePath
	})
	if document.Finetuning != nil {
		fine := *document.Finetuning
		fine.BaseModelVersionID = strings.ToLower(fine.BaseModelVersionID)
		fine.ExportProgram.ImageVersionID = strings.ToLower(fine.ExportProgram.ImageVersionID)
		fine.VerifyProgram.ImageVersionID = strings.ToLower(fine.VerifyProgram.ImageVersionID)
		document.Finetuning = &fine
	}
	return encodeCanonicalJSON(document)
}

func canonicalParameterRules(values []ParameterRule) []ParameterRule {
	if values == nil {
		return nil
	}
	rules := append([]ParameterRule{}, values...)
	for i := range rules {
		rules[i].AllowedValues = canonicalParameters(rules[i].AllowedValues)
		if rules[i].Minimum != nil {
			value := *rules[i].Minimum
			value.Value = canonicalDecimal(value.Value)
			rules[i].Minimum = &value
		}
		if rules[i].Maximum != nil {
			value := *rules[i].Maximum
			value.Value = canonicalDecimal(value.Value)
			rules[i].Maximum = &value
		}
		sort.Slice(rules[i].AllowedValues, func(a, b int) bool { return rules[i].AllowedValues[a].Value < rules[i].AllowedValues[b].Value })
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules
}

func invalidReleaseDocument(document ReleaseDocument) string {
	if document.SchemaVersion != ReleaseSchemaVersion {
		return "schema_version"
	}
	if (document.Kind != "GENERAL_TRAINING" && document.Kind != "FINETUNING") || document.DeliveryMode != "SAVE_ARTIFACTS" {
		return "capability"
	}
	for _, value := range []string{document.ReleaseID, document.PresetID, document.PipelineID, document.PipelineVersionID, document.Program.ImageVersionID} {
		if !validUUID(value) {
			return "fixed identity"
		}
	}
	if !snapshotSHA256Pattern.MatchString(document.PipelineIRSHA256) || !snapshotSHA256Pattern.MatchString(document.Runtime.ContentSHA256) {
		return "content sha256"
	}
	if field := invalidRuntimeRef(document.Runtime); field != "" {
		return field
	}
	if document.Kind == "FINETUNING" {
		return invalidFinetuningRelease(document)
	}
	if document.Finetuning != nil || len(document.Program.ParameterRules) != 0 || len(document.Program.AllowedParameterSets) != 0 {
		return "capability"
	}
	program := document.Program
	if !validSnapshotImage(program.ImageDigest) {
		return "image digest"
	}
	if !slices.Equal(program.Command, []string{"/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"}) {
		return "command"
	}
	arguments := []ReleaseArgument{
		{Literal: "--data"}, {Source: "INPUT_PATH"},
		{Literal: "--output"}, {Source: "OUTPUT_PATH"},
		{Literal: "--expected-input-sha256"}, {Source: "INPUT_SHA256"},
		{Literal: "--expected-input-bytes"}, {Source: "INPUT_BYTES"},
		{Literal: "--learning-rate"}, {Source: "LEARNING_RATE"},
	}
	if len(program.ArgsTemplate) != len(arguments) && len(program.ArgsTemplate) != len(arguments)+2 {
		return "args template"
	}
	if !slices.Equal(program.ArgsTemplate[:len(arguments)], arguments) {
		return "args template"
	}
	if len(program.ArgsTemplate) > len(arguments) {
		flag, recipe := program.ArgsTemplate[len(arguments)], program.ArgsTemplate[len(arguments)+1]
		if flag != (ReleaseArgument{Literal: "--recipe"}) || recipe.Source != "" || (recipe.Literal != "success" && recipe.Literal != "fail" && recipe.Literal != "slow-stop") {
			return "managed recipe"
		}
	}
	if program.ParameterContractVersion != "ani.cpu.mlp.parameters.v1" || len(program.DefaultParameters) != 3 || validateRegisteredParameters(program.DefaultParameters) != nil {
		return "parameter defaults"
	}
	if field := invalidCPUResources(document.Resources); field != "" {
		return field
	}
	if field := invalidWorkspaceContract(document.Workspace); field != "" {
		return field
	}
	if field := invalidOutputContract(document.OutputContract); field != "" {
		return field
	}
	if document.ExecutionTimeoutSeconds == 0 {
		return "execution timeout"
	}
	return ""
}
