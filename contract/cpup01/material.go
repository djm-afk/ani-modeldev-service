package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

const MaterialSchemaVersion = "ani.modeldev.implementation-material.v2.1"
const MaxMaterialBytes = 64 * 1024

// ImplementationMaterial fixes finite software and safety provenance. It has
// no catalogue binding, approval state, training preset or parameter whitelist.
// Resources and timing are compiled for each execution from their real owners.
type ImplementationMaterial struct {
	SchemaVersion  string                 `json:"schema_version"`
	Implementation ImplementationSnapshot `json:"implementation"`
	Program        MaterialProgram        `json:"program"`
	Workspace      WorkspaceContract      `json:"workspace"`
	OutputContract OutputContract         `json:"output_contract"`
	Finetuning     *FinetuningMaterial    `json:"finetuning,omitempty"`
}

type MaterialProgram struct {
	ImageVersionID string             `json:"image_version_id"`
	ImageDigest    string             `json:"image_digest"`
	Command        []string           `json:"command"`
	ArgsTemplate   []MaterialArgument `json:"args_template"`
}

type MaterialArgument struct {
	Literal string `json:"literal,omitempty"`
	Source  string `json:"source,omitempty"`
}

type ComponentProgram struct {
	ImageVersionID       string             `json:"image_version_id"`
	ImageDigest          string             `json:"image_digest"`
	Command              []string           `json:"command"`
	ArgsTemplate         []MaterialArgument `json:"args_template"`
	ImplementationSHA256 string             `json:"implementation_sha256"`
}

type FinetuningMaterial struct {
	ManagedSettings   FinetuningManagedSettings `json:"managed_settings"`
	ExportProgram     ComponentProgram          `json:"export_program"`
	VerifyProgram     ComponentProgram          `json:"verify_program"`
	RecipeSHA256      string                    `json:"recipe_sha256"`
	MappingSHA256     string                    `json:"mapping_sha256"`
	Budgets           ComponentBudgets          `json:"budgets"`
	MaxSequenceLength int64                     `json:"max_sequence_length"`
}

func ParseMaterial(raw []byte) (ImplementationMaterial, string, error) {
	bad := func() (ImplementationMaterial, string, error) {
		return ImplementationMaterial{}, "", fmt.Errorf("%w: implementation material", ErrInvalidArgument)
	}
	if len(raw) == 0 || len(raw) > MaxMaterialBytes || !validUnicodeEscapes(raw) {
		return bad()
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	if checkJSONValue(scanner, 0) != nil {
		return bad()
	}
	var m ImplementationMaterial
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return bad()
	}
	if d.Decode(new(any)) != io.EOF {
		return bad()
	}
	canonical, err := m.Canonical()
	if err != nil || !bytes.Equal(canonical, raw) {
		return bad()
	}
	sum := sha256.Sum256(canonical)
	return m, hex.EncodeToString(sum[:]), nil
}

func (m ImplementationMaterial) Canonical() ([]byte, error) {
	definition, ok := Implementations()[m.Implementation.ImplementationID]
	if !ok || m.SchemaVersion != MaterialSchemaVersion || m.Implementation.Version != definition.Version || m.Implementation.ParameterContractVersion != definition.ParameterContractVersion || !canonicalMaterialID(m.Implementation.PipelineID) || !canonicalMaterialID(m.Implementation.PipelineVersionID) || !snapshotSHA256Pattern.MatchString(m.Implementation.PipelineIRSHA256) || !canonicalMaterialID(m.Program.ImageVersionID) || !validSnapshotImage(m.Program.ImageDigest) || !validMaterialProgram(m.Program.Command, m.Program.ArgsTemplate, definition.Kind == "FINETUNING") || invalidWorkspaceContract(m.Workspace) != "" || invalidOutputContractForKind(definition.Kind, m.OutputContract) != "" {
		return nil, ErrInvalidArgument
	}
	if (definition.Kind == "FINETUNING") != (m.Finetuning != nil) {
		return nil, ErrInvalidArgument
	}
	if m.Finetuning != nil {
		f := m.Finetuning
		if invalidFinetuningManagedSettings(f.ManagedSettings) != "" || invalidComponentBudgets(f.Budgets) != "" || f.MaxSequenceLength <= 0 || !snapshotSHA256Pattern.MatchString(f.RecipeSHA256) || !snapshotSHA256Pattern.MatchString(f.MappingSHA256) {
			return nil, ErrInvalidArgument
		}
		for _, p := range []ComponentProgram{f.ExportProgram, f.VerifyProgram} {
			if !canonicalMaterialID(p.ImageVersionID) || !validSnapshotImage(p.ImageDigest) || !snapshotSHA256Pattern.MatchString(p.ImplementationSHA256) || !validMaterialProgram(p.Command, p.ArgsTemplate, true) {
				return nil, ErrInvalidArgument
			}
		}
	}
	return encodeCanonicalJSON(m)
}

func canonicalMaterialID(id string) bool { return validUUID(id) && strings.ToLower(id) == id }
func validMaterialProgram(command []string, args []MaterialArgument, finetuning bool) bool {
	if len(command) == 0 || len(command) > 32 || len(args) > 256 {
		return false
	}
	for _, v := range command {
		if !validSnapshotText(v) {
			return false
		}
	}
	switch path.Base(command[0]) {
	case "sh", "bash", "dash", "zsh", "ksh", "env", "busybox":
		return false
	}
	for _, a := range args {
		if (a.Literal == "") == (a.Source == "") {
			return false
		}
		if a.Literal != "" {
			if !validSnapshotText(a.Literal) {
				return false
			}
			continue
		}
		if finetuning {
			switch a.Source {
			case "INPUT_SHA256", "INPUT_BYTES", "BASE_MANIFEST_SHA256":
			default:
				return false
			}
		} else {
			switch a.Source {
			case "INPUT_PATH", "OUTPUT_PATH", "INPUT_SHA256", "INPUT_BYTES", "LEARNING_RATE", "EPOCHS", "BATCH_SIZE", "SEED":
			default:
				return false
			}
		}
	}
	return true
}
