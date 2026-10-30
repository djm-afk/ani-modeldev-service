package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

const MaterialSchemaVersion = "ani.modeldev.implementation-material.v3"

// ImplementationMaterial fixes finite software and safety provenance. It has
// no catalogue binding, approval state, training preset or parameter whitelist.
// Resources and timing are compiled for each execution from their real owners.
type ImplementationMaterial struct {
	SchemaVersion  string                 `json:"schema_version"`
	Implementation ImplementationSnapshot `json:"implementation"`
	// Program is the finetuning chain's frozen image identity. General training
	// selects from the catalogue instead, so exactly one of Program/Training is
	// present and the two chains never share a material shape.
	Program            *MaterialProgram              `json:"program,omitempty"`
	Training           *TrainingMaterial             `json:"training,omitempty"`
	Workspace          WorkspaceContract             `json:"workspace"`
	ComponentResources map[string]ComponentResources `json:"component_resources"`
	OutputContract     OutputContract                `json:"output_contract"`
	Finetuning         *FinetuningMaterial           `json:"finetuning,omitempty"`
}

// TrainingMaterial is the trusted general-training contract. The image is a
// catalogue default resolved by Resource, so a stale material cannot pin an old
// digest; the command, argument template and output contract stay fixed.
type TrainingMaterial struct {
	DefaultImage DefaultTrainingImage `json:"default_image"`
	Command      []string             `json:"command"`
	ArgsTemplate []MaterialArgument   `json:"args_template"`
}

type MaterialProgram struct {
	ImageID      string             `json:"image_id"`
	ImageVersion int64              `json:"image_version,string"`
	ImageScope   ImageScope         `json:"image_scope"`
	ImageDigest  string             `json:"image_digest"`
	Command      []string           `json:"command"`
	ArgsTemplate []MaterialArgument `json:"args_template"`
}

type MaterialArgument struct {
	Literal string `json:"literal,omitempty"`
	Source  string `json:"source,omitempty"`
}

type ComponentProgram struct {
	ImageID              string             `json:"image_id"`
	ImageVersion         int64              `json:"image_version,string"`
	ImageScope           ImageScope         `json:"image_scope"`
	ImageDigest          string             `json:"image_digest"`
	Command              []string           `json:"command"`
	ArgsTemplate         []MaterialArgument `json:"args_template"`
	ImplementationSHA256 string             `json:"implementation_sha256"`
}

type FinetuningMaterial struct {
	ManagedSettings           FinetuningManagedSettings  `json:"managed_settings"`
	ExportProgram             ComponentProgram           `json:"export_program"`
	VerifyProgram             ComponentProgram           `json:"verify_program"`
	RecipeSHA256              string                     `json:"recipe_sha256"`
	MappingSHA256             string                     `json:"mapping_sha256"`
	RuntimeCapabilities       RuntimeCapabilities        `json:"runtime_capabilities"`
	ModelTemplateAssociations []ModelTemplateAssociation `json:"model_template_associations"`
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
	if !ok || m.SchemaVersion != MaterialSchemaVersion || m.Implementation.Version != definition.Version || m.Implementation.ParameterContractVersion != definition.ParameterContractVersion || !canonicalMaterialID(m.Implementation.PipelineID) || !canonicalMaterialID(m.Implementation.PipelineVersionID) || !snapshotSHA256Pattern.MatchString(m.Implementation.PipelineIRSHA256) || invalidWorkspaceContract(m.Workspace) != "" || invalidOutputContractForKind(definition.Kind, m.OutputContract) != "" {
		return nil, ErrInvalidArgument
	}
	if definition.Kind == "GENERAL_TRAINING" {
		if m.Training == nil || m.Program != nil || m.Finetuning != nil {
			return nil, ErrInvalidArgument
		}
		if m.Training.DefaultImage.validate() != nil || !validMaterialProgram(m.Training.Command, m.Training.ArgsTemplate, false) {
			return nil, ErrInvalidArgument
		}
	} else {
		if m.Program == nil || m.Training != nil || m.Finetuning == nil {
			return nil, ErrInvalidArgument
		}
		if !validRegisteredImage(m.Program.ImageID, m.Program.ImageVersion, m.Program.ImageScope) || !validSnapshotImage(m.Program.ImageDigest) || !validMaterialProgram(m.Program.Command, m.Program.ArgsTemplate, true) {
			return nil, ErrInvalidArgument
		}
	}
	if ValidateComponentResources(definition.Kind, m.ComponentResources) != nil {
		return nil, ErrInvalidArgument
	}
	if m.Finetuning != nil {
		frozen := *m.Finetuning
		m.Finetuning = &frozen
		f := m.Finetuning
		f.ModelTemplateAssociations = append([]ModelTemplateAssociation{}, f.ModelTemplateAssociations...)
		capabilities, err := f.RuntimeCapabilities.Canonical()
		if err != nil || json.Unmarshal(capabilities, &f.RuntimeCapabilities) != nil {
			return nil, ErrInvalidArgument
		}
		if invalidFinetuningManagedSettings(f.ManagedSettings) != "" || !snapshotSHA256Pattern.MatchString(f.RecipeSHA256) || !snapshotSHA256Pattern.MatchString(f.MappingSHA256) {
			return nil, ErrInvalidArgument
		}
		if _, err := f.RuntimeCapabilities.Canonical(); err != nil || f.RuntimeCapabilities.ImageDigest != m.Program.ImageDigest {
			return nil, ErrInvalidArgument
		}
		seen := map[string]bool{}
		for _, association := range f.ModelTemplateAssociations {
			if !validUUID(association.ModelVersionID) || !validTemplateID(association.TemplateID) || seen[association.ModelVersionID] {
				return nil, ErrInvalidArgument
			}
			seen[association.ModelVersionID] = true
			found := false
			for _, t := range f.RuntimeCapabilities.RegisteredTemplates {
				if t.ID == association.TemplateID {
					found = true
					break
				}
			}
			if !found {
				return nil, ErrInvalidArgument
			}
		}
		if invalidFinetuningArguments(m.Program.ArgsTemplate, "train") != "" || invalidFinetuningArguments(f.ExportProgram.ArgsTemplate, "export") != "" || invalidFinetuningArguments(f.VerifyProgram.ArgsTemplate, "verify") != "" {
			return nil, ErrInvalidArgument
		}
		for _, p := range []ComponentProgram{f.ExportProgram, f.VerifyProgram} {
			if p.ImageDigest != m.Program.ImageDigest || !slices.Equal(p.Command, m.Program.Command) || !validRegisteredImage(p.ImageID, p.ImageVersion, p.ImageScope) || !validSnapshotImage(p.ImageDigest) || !snapshotSHA256Pattern.MatchString(p.ImplementationSHA256) || !validMaterialProgram(p.Command, p.ArgsTemplate, true) {
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
			case "DATASET_MANIFEST_SHA256", "BASE_MANIFEST_SHA256", "TEMPLATE_PLAN_SHA256":
			default:
				return false
			}
		} else {
			switch a.Source {
			case "OUTPUT_PATH", "DATASET_MANIFEST_SHA256", "LEARNING_RATE", "EPOCHS", "BATCH_SIZE", "SEED":
			default:
				return false
			}
		}
	}
	return true
}
