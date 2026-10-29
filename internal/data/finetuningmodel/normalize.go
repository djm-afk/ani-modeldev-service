package finetuningmodel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// NormalizeModelFacts derives only descriptors from provider-verified bytes.
// It never reads a current model default or executes a template.
func NormalizeModelFacts(files []cpup01.BaseModelFile, config, tokenizer, index []byte, capabilities cpup01.RuntimeCapabilities) (cpup01.NormalizedModelFacts, error) {
	invalid := func() (cpup01.NormalizedModelFacts, error) {
		return cpup01.NormalizedModelFacts{}, biz.ErrBaseModelInvalid
	}
	if _, err := capabilities.Canonical(); err != nil {
		return cpup01.NormalizedModelFacts{}, biz.ErrModelDependencyUnavailable
	}
	inventory := map[string]cpup01.BaseModelFile{}
	for _, f := range files {
		if _, seen := inventory[f.Path]; seen {
			return invalid()
		}
		inventory[f.Path] = f
	}
	objects := map[string]map[string]json.RawMessage{}
	for name, raw := range map[string][]byte{"config.json": config, "tokenizer_config.json": tokenizer} {
		file, ok := inventory[name]
		sum := sha256.Sum256(raw)
		if !ok || len(raw) == 0 || len(raw) > cpup01.MaxModelConfigBytes || file.SizeBytes != int64(len(raw)) || file.SHA256 != hex.EncodeToString(sum[:]) || !strictJSONObject(raw) {
			return invalid()
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return invalid()
		}
		objects[name] = object
	}
	result := cpup01.NormalizedModelFacts{Architectures: []string{}, ContextSources: []cpup01.ContextLimitFact{}, TemplateSources: []cpup01.TemplateSource{}}
	model := objects["config.json"]
	if json.Unmarshal(model["model_type"], &result.ModelType) != nil {
		return invalid()
	}
	if declared, ok := model["architectures"]; ok && string(declared) != "null" {
		if json.Unmarshal(declared, &result.Architectures) != nil {
			return invalid()
		}
		for _, architecture := range result.Architectures {
			if strings.TrimSpace(architecture) == "" {
				return invalid()
			}
		}
	}
	if !slices.Contains(capabilities.CausalLMModelTypes, result.ModelType) {
		return cpup01.NormalizedModelFacts{}, biz.ErrBaseModelUnsupported
	}
	for _, name := range []string{"torch_dtype", "dtype"} {
		if value, ok := model[name]; ok && string(value) != "null" {
			if json.Unmarshal(value, &result.OriginalDtype) != nil {
				// Native composite dtype metadata remains descriptive. The selected
				// runtime owns loading and the frozen training precision.
				var compact bytes.Buffer
				if json.Compact(&compact, value) != nil {
					return invalid()
				}
				result.OriginalDtype = compact.String()
			}
			break
		}
	}
	result.ConfigFile = fileFact(inventory["config.json"])
	result.TokenizerConfigFile = fileFact(inventory["tokenizer_config.json"])
	fields := []cpup01.ContextLimitField{{Source: "config.json", Field: "max_position_embeddings"}, {Source: "config.json", Field: "n_positions"}, {Source: "tokenizer_config.json", Field: "model_max_length"}}
	fields = append(fields, capabilities.ContextLimitFields...)
	seen := map[string]bool{}
	for _, field := range fields {
		key := field.Source + ":" + field.Field
		if seen[key] {
			continue
		}
		seen[key] = true
		raw, declared := objects[field.Source][field.Field]
		if !declared {
			continue
		}
		value, sentinel, err := contextInteger(raw, field.Source == "tokenizer_config.json" && field.Field == "model_max_length")
		if err != nil {
			return invalid()
		}
		if sentinel {
			continue
		}
		result.ContextSources = append(result.ContextSources, cpup01.ContextLimitFact{SourcePath: field.Source, SourceSHA256: inventory[field.Source].SHA256, Field: field.Field, Value: value})
		if result.ContextLimit == 0 || value < result.ContextLimit {
			result.ContextLimit = value
		}
	}
	if result.ContextLimit == 0 {
		return cpup01.NormalizedModelFacts{}, biz.ErrContextLimitUnknown
	}
	sort.Slice(result.ContextSources, func(i, j int) bool {
		return result.ContextSources[i].SourcePath+":"+result.ContextSources[i].Field < result.ContextSources[j].SourcePath+":"+result.ContextSources[j].Field
	})
	layout, err := NormalizeWeightLayout(files, index)
	if err != nil {
		return invalid()
	}
	result.WeightLayout = layout
	for _, f := range files {
		if f.Path == "chat_template.jinja" || path.Dir(f.Path) == "additional_chat_templates" && path.Ext(f.Path) == ".jinja" {
			name := strings.TrimSuffix(path.Base(f.Path), ".jinja")
			if f.Path == "chat_template.jinja" {
				name = "default"
			}
			result.TemplateSources = append(result.TemplateSources, cpup01.TemplateSource{Kind: "FILE", Path: f.Path, Name: name, Default: name == "default", SizeBytes: f.SizeBytes, SHA256: f.SHA256})
		}
	}
	if raw, ok := objects["tokenizer_config.json"]["chat_template"]; ok && string(raw) != "null" {
		embedded, err := embeddedTemplates(raw)
		if err != nil {
			return invalid()
		}
		result.TemplateSources = append(result.TemplateSources, embedded...)
	}
	sort.Slice(result.TemplateSources, func(i, j int) bool {
		a, b := result.TemplateSources[i], result.TemplateSources[j]
		return a.Kind+":"+a.Path+":"+a.Name < b.Kind+":"+b.Path+":"+b.Name
	})
	if result.ValidateFor(files) != nil {
		return invalid()
	}
	return result, nil
}
func fileFact(f cpup01.BaseModelFile) cpup01.FileFact {
	return cpup01.FileFact{Path: f.Path, SizeBytes: f.SizeBytes, SHA256: f.SHA256}
}

func contextInteger(raw []byte, tokenizerSentinel bool) (int64, bool, error) {
	text := string(raw)
	if text == "" || strings.IndexFunc(text, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, false, biz.ErrBaseModelInvalid
	}
	if tokenizerSentinel && (len(text) > 19 || len(text) == 19 && text >= "9223372036854775807") {
		return 0, true, nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return 0, false, biz.ErrBaseModelInvalid
	}
	return value, false, nil
}
func embeddedTemplates(raw []byte) ([]cpup01.TemplateSource, error) {
	templates := map[string]string{}
	switch raw[0] {
	case '"':
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, biz.ErrBaseModelInvalid
		}
		if text != "" {
			templates["default"] = text
		}
	case '[':
		var values []struct {
			Name     string `json:"name"`
			Template string `json:"template"`
		}
		if json.Unmarshal(raw, &values) != nil {
			return nil, biz.ErrBaseModelInvalid
		}
		for _, v := range values {
			if v.Name == "" || v.Template == "" {
				return nil, biz.ErrBaseModelInvalid
			}
			if _, ok := templates[v.Name]; ok {
				return nil, biz.ErrBaseModelInvalid
			}
			templates[v.Name] = v.Template
		}
	case '{':
		if json.Unmarshal(raw, &templates) != nil {
			return nil, biz.ErrBaseModelInvalid
		}
	default:
		return nil, biz.ErrBaseModelInvalid
	}
	result := []cpup01.TemplateSource{}
	for name, text := range templates {
		if name == "" || text == "" || !utf8.ValidString(text) {
			return nil, biz.ErrBaseModelInvalid
		}
		sum := sha256.Sum256([]byte(text))
		result = append(result, cpup01.TemplateSource{Kind: "EMBEDDED", Path: "tokenizer_config.json", Name: name, Default: name == "default", SizeBytes: int64(len(text)), SHA256: hex.EncodeToString(sum[:])})
	}
	return result, nil
}

func strictJSONObject(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > cpup01.MaxJSONDepth {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return false
				}
				name, ok := token.(string)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
