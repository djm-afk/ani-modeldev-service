package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
)

const FinetuningCapabilitiesSchemaVersion = "ani.modeldev.finetuning-capabilities.v3"
const TemplatePlanSchemaVersion = "ani.modeldev.template-plan.v3"
const EffectiveTemplateSchemaVersion = "ani.modeldev.effective-template.v3"

// RuntimeCapabilities is extracted inside the final immutable image. It is
// evidence of the native runtime's capabilities, never a model-name allowlist.
type RuntimeCapabilities struct {
	SchemaVersion       string               `json:"schema_version"`
	ImageDigest         string               `json:"image_digest"`
	EntrypointSHA256    string               `json:"entrypoint_sha256"`
	Packages            []RuntimePackage     `json:"packages"`
	CausalLMModelTypes  []string             `json:"causal_lm_model_types"`
	RegisteredTemplates []RegisteredTemplate `json:"registered_templates"`
	ContextLimitFields  []ContextLimitField  `json:"context_limit_fields"`
}

type RuntimePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type RegisteredTemplate struct {
	ID                string `json:"id"`
	SourceSHA256      string `json:"source_sha256"`
	SupportsSystem    bool   `json:"supports_system"`
	SupportsMultiTurn bool   `json:"supports_multi_turn"`
}
type ContextLimitField struct {
	Source string `json:"source"`
	Field  string `json:"field"`
}

// FileFact refers to bytes proven by the source inventory; it never embeds
// model configuration or an executable template in a control-plane document.
type FileFact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes,string"`
	SHA256    string `json:"sha256"`
}
type ContextLimitFact struct {
	SourcePath   string `json:"source_path"`
	SourceSHA256 string `json:"source_sha256"`
	Field        string `json:"field"`
	Value        int64  `json:"value,string"`
}
type WeightLayout struct {
	Kind       string   `json:"kind"`
	EntryPath  string   `json:"entry_path"`
	ShardPaths []string `json:"shard_paths"`
}
type TemplateSource struct {
	Kind      string `json:"kind"` // FILE or EMBEDDED
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	Default   bool   `json:"default"`
	SizeBytes int64  `json:"size_bytes,string"`
	SHA256    string `json:"sha256"`
}
type NormalizedModelFacts struct {
	ModelType           string             `json:"model_type"`
	Architectures       []string           `json:"architectures"`
	OriginalDtype       string             `json:"original_dtype"`
	ContextLimit        int64              `json:"context_limit,string"`
	ContextSources      []ContextLimitFact `json:"context_sources"`
	ConfigFile          FileFact           `json:"config_file"`
	TokenizerConfigFile FileFact           `json:"tokenizer_config_file"`
	WeightLayout        WeightLayout       `json:"weight_layout"`
	TemplateSources     []TemplateSource   `json:"template_sources"`
}
type ModelTemplateAssociation struct {
	ModelVersionID string `json:"model_version_id"`
	TemplateID     string `json:"template_id"`
}

type TemplatePlan struct {
	SchemaVersion             string          `json:"schema_version"`
	Mode                      string          `json:"mode"`             // NAMED or TOKENIZER_NATIVE
	SelectionSource           string          `json:"selection_source"` // USER, MODEL_ASSOCIATION, MODEL_TOKENIZER
	TemplateID                *string         `json:"template_id,omitempty"`
	Source                    *TemplateSource `json:"source,omitempty"`
	SourceSHA256              string          `json:"source_sha256"`
	RuntimeCapabilitiesSHA256 string          `json:"runtime_capabilities_sha256"`
	ImageDigest               string          `json:"image_digest"`
}

type SpecialToken struct {
	Name  string `json:"name"`
	Token string `json:"token"`
	ID    int64  `json:"id,string"`
}
type TemplateEncodingOptions struct {
	AddBOS     bool `json:"add_bos"`
	AddEOS     bool `json:"add_eos"`
	ReplaceEOS bool `json:"replace_eos"`
}
type EffectiveTemplate struct {
	SchemaVersion             string                  `json:"schema_version"`
	TemplatePlanSHA256        string                  `json:"template_plan_sha256"`
	RuntimeCapabilitiesSHA256 string                  `json:"runtime_capabilities_sha256"`
	ImageDigest               string                  `json:"image_digest"`
	SourceSHA256              string                  `json:"source_sha256"`
	TemplateFile              FileFact                `json:"template_file"`
	VocabSize                 int64                   `json:"vocab_size,string"`
	SpecialTokens             []SpecialToken          `json:"special_tokens"`
	EOSIDs                    []int64                 `json:"eos_ids"`
	EncodingOptions           TemplateEncodingOptions `json:"encoding_options"`
}

var topLevelIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateTemplateID(value string) error {
	if !validTemplateID(value) {
		return fmt.Errorf("%w: template ID", ErrInvalidArgument)
	}
	return nil
}

func contractDigest(raw []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (c RuntimeCapabilities) Canonical() ([]byte, error) {
	bad := func() ([]byte, error) { return nil, fmt.Errorf("%w: runtime capabilities", ErrInvalidArgument) }
	if c.SchemaVersion != FinetuningCapabilitiesSchemaVersion || !validSnapshotImage(c.ImageDigest) || !snapshotSHA256Pattern.MatchString(c.EntrypointSHA256) || len(c.Packages) == 0 || len(c.CausalLMModelTypes) == 0 || len(c.ContextLimitFields) == 0 {
		return bad()
	}
	c.Packages = slices.Clone(c.Packages)
	c.CausalLMModelTypes = slices.Clone(c.CausalLMModelTypes)
	c.RegisteredTemplates = append([]RegisteredTemplate{}, c.RegisteredTemplates...)
	c.ContextLimitFields = slices.Clone(c.ContextLimitFields)
	seen := map[string]bool{}
	for _, p := range c.Packages {
		if !validSnapshotText(p.Name) || !validSnapshotText(p.Version) || seen[p.Name] {
			return bad()
		}
		seen[p.Name] = true
	}
	clear(seen)
	for _, v := range c.CausalLMModelTypes {
		if !validSnapshotText(v) || seen[v] {
			return bad()
		}
		seen[v] = true
	}
	clear(seen)
	for _, v := range c.RegisteredTemplates {
		if !templateIDPattern.MatchString(v.ID) || !snapshotSHA256Pattern.MatchString(v.SourceSHA256) || seen[v.ID] {
			return bad()
		}
		seen[v.ID] = true
	}
	clear(seen)
	for _, v := range c.ContextLimitFields {
		key := v.Source + ":" + v.Field
		if (v.Source != "config.json" && v.Source != "tokenizer_config.json") || !topLevelIdentifier.MatchString(v.Field) || seen[key] {
			return bad()
		}
		seen[key] = true
	}
	sort.Slice(c.Packages, func(i, j int) bool { return c.Packages[i].Name < c.Packages[j].Name })
	sort.Strings(c.CausalLMModelTypes)
	sort.Slice(c.RegisteredTemplates, func(i, j int) bool { return c.RegisteredTemplates[i].ID < c.RegisteredTemplates[j].ID })
	sort.Slice(c.ContextLimitFields, func(i, j int) bool {
		return c.ContextLimitFields[i].Source+":"+c.ContextLimitFields[i].Field < c.ContextLimitFields[j].Source+":"+c.ContextLimitFields[j].Field
	})
	raw, err := encodeCanonicalJSON(c)
	if len(raw) > MaxMaterialBytes {
		return bad()
	}
	return raw, err
}
func (c RuntimeCapabilities) Digest() (string, error) { return contractDigest(c.Canonical()) }
func (p TemplatePlan) Canonical() ([]byte, error) {
	if p.SchemaVersion != TemplatePlanSchemaVersion || !validSnapshotImage(p.ImageDigest) || !snapshotSHA256Pattern.MatchString(p.RuntimeCapabilitiesSHA256) || !snapshotSHA256Pattern.MatchString(p.SourceSHA256) {
		return nil, fmt.Errorf("%w: template plan", ErrInvalidArgument)
	}
	switch p.Mode {
	case "NAMED":
		if p.TemplateID == nil || !templateIDPattern.MatchString(*p.TemplateID) || p.Source != nil || (p.SelectionSource != "USER" && p.SelectionSource != "MODEL_ASSOCIATION") {
			return nil, ErrInvalidArgument
		}
	case "TOKENIZER_NATIVE":
		if p.TemplateID != nil || p.Source == nil || p.SelectionSource != "MODEL_TOKENIZER" || p.Source.Validate() != nil || p.Source.SHA256 != p.SourceSHA256 {
			return nil, ErrInvalidArgument
		}
	default:
		return nil, ErrInvalidArgument
	}
	return encodeCanonicalJSON(p)
}
func (p TemplatePlan) Digest() (string, error) { return contractDigest(p.Canonical()) }
func (p TemplatePlan) ValidateFor(c RuntimeCapabilities) error {
	if _, err := p.Canonical(); err != nil {
		return err
	}
	digest, err := c.Digest()
	if err != nil || digest != p.RuntimeCapabilitiesSHA256 || p.ImageDigest != c.ImageDigest {
		return ErrInvalidArgument
	}
	if p.Mode == "NAMED" {
		for _, t := range c.RegisteredTemplates {
			if t.ID == *p.TemplateID && t.SourceSHA256 == p.SourceSHA256 {
				return nil
			}
		}
		return ErrInvalidArgument
	}
	return nil
}
func (s TemplateSource) Validate() error {
	if (s.Kind != "FILE" && s.Kind != "EMBEDDED") || !validSnapshotPath(s.Path) || s.SizeBytes <= 0 || s.SizeBytes > MaxModelConfigBytes || !snapshotSHA256Pattern.MatchString(s.SHA256) {
		return ErrInvalidArgument
	}
	if s.Kind == "EMBEDDED" && s.Path != "tokenizer_config.json" {
		return ErrInvalidArgument
	}
	if s.Kind == "FILE" && s.Path != "chat_template.jinja" && path.Dir(s.Path) != "additional_chat_templates" {
		return ErrInvalidArgument
	}
	return nil
}

// ValidateFor checks frozen relationships only. Provider readers prove actual
// JSON/index bytes; decoding a snapshot never consults the current registry.
func (f NormalizedModelFacts) ValidateFor(files []BaseModelFile) error {
	if !validSnapshotText(f.ModelType) || f.ContextLimit <= 0 || len(f.ContextSources) == 0 {
		return ErrInvalidArgument
	}
	for _, architecture := range f.Architectures {
		if !validSnapshotText(architecture) {
			return ErrInvalidArgument
		}
	}
	inventory := map[string]BaseModelFile{}
	for _, file := range files {
		if !validSnapshotPath(file.Path) || len(file.Path) > MaxRelativePathBytes || file.SizeBytes < 0 || file.SizeBytes == 0 && file.SHA256 != EmptyContentSHA256 || !snapshotSHA256Pattern.MatchString(file.SHA256) {
			return ErrInvalidArgument
		}
		if _, ok := inventory[file.Path]; ok {
			return ErrInvalidArgument
		}
		inventory[file.Path] = file
	}
	for _, ref := range []FileFact{f.ConfigFile, f.TokenizerConfigFile} {
		original, ok := inventory[ref.Path]
		if !ok || ref.SizeBytes <= 0 || ref.SizeBytes > MaxModelConfigBytes || original.SizeBytes != ref.SizeBytes || original.SHA256 != ref.SHA256 {
			return ErrInvalidArgument
		}
	}
	if f.ConfigFile.Path != "config.json" || f.TokenizerConfigFile.Path != "tokenizer_config.json" {
		return ErrInvalidArgument
	}
	minimum := int64(0)
	seen := map[string]bool{}
	for _, value := range f.ContextSources {
		source, ok := inventory[value.SourcePath]
		key := value.SourcePath + ":" + value.Field
		if !ok || source.SHA256 != value.SourceSHA256 || !topLevelIdentifier.MatchString(value.Field) || value.Value <= 0 || seen[key] {
			return ErrInvalidArgument
		}
		seen[key] = true
		if minimum == 0 || value.Value < minimum {
			minimum = value.Value
		}
	}
	if minimum != f.ContextLimit {
		return ErrInvalidArgument
	}
	layout := f.WeightLayout
	switch layout.Kind {
	case "SINGLE":
		if layout.EntryPath != "model.safetensors" || len(layout.ShardPaths) != 0 {
			return ErrInvalidArgument
		}
		if _, ambiguous := inventory["model.safetensors.index.json"]; ambiguous {
			return ErrInvalidArgument
		}
	case "SHARDED":
		if layout.EntryPath != "model.safetensors.index.json" || len(layout.ShardPaths) == 0 {
			return ErrInvalidArgument
		}
		if _, ambiguous := inventory["model.safetensors"]; ambiguous {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	entry, ok := inventory[layout.EntryPath]
	if !ok || entry.SizeBytes <= 0 {
		return ErrInvalidArgument
	}
	clear(seen)
	for _, name := range layout.ShardPaths {
		file, ok := inventory[name]
		if !ok || file.SizeBytes <= 0 || seen[name] || path.Ext(name) != ".safetensors" {
			return ErrInvalidArgument
		}
		seen[name] = true
	}
	for _, source := range f.TemplateSources {
		if source.Validate() != nil {
			return ErrInvalidArgument
		}
		file, ok := inventory[source.Path]
		if !ok || file.SizeBytes <= 0 {
			return ErrInvalidArgument
		}
		if source.Kind == "FILE" && (file.SHA256 != source.SHA256 || file.SizeBytes != source.SizeBytes) {
			return ErrInvalidArgument
		}
	}
	return nil
}

func templateMatchesIntent(intent Intent, plan TemplatePlan) bool {
	if intent.FinetuningTemplate == nil {
		return plan.SelectionSource != "USER"
	}
	return plan.SelectionSource == "USER" && plan.Mode == "NAMED" && plan.TemplateID != nil && *intent.FinetuningTemplate == *plan.TemplateID
}
