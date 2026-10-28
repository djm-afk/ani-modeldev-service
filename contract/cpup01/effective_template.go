package cpup01

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"unicode/utf8"
)

const EffectiveTemplateRelativePath = "reports/train/effective-template.jinja"
const EmptyContentSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (e EffectiveTemplate) Canonical() ([]byte, error) {
	if e.SchemaVersion != EffectiveTemplateSchemaVersion || !snapshotSHA256Pattern.MatchString(e.TemplatePlanSHA256) || !snapshotSHA256Pattern.MatchString(e.RuntimeCapabilitiesSHA256) || !snapshotSHA256Pattern.MatchString(e.SourceSHA256) || !validSnapshotImage(e.ImageDigest) || e.TemplateFile.Path != EffectiveTemplateRelativePath || e.TemplateFile.SizeBytes <= 0 || e.TemplateFile.SizeBytes > MaxModelConfigBytes || !snapshotSHA256Pattern.MatchString(e.TemplateFile.SHA256) || len(e.EOSIDs) == 0 || e.VocabSize <= 0 {
		return nil, ErrInvalidArgument
	}
	e.SpecialTokens = append([]SpecialToken{}, e.SpecialTokens...)
	e.EOSIDs = append([]int64{}, e.EOSIDs...)
	names := map[string]bool{}
	ids := map[int64]string{}
	for _, token := range e.SpecialTokens {
		if !validSnapshotText(token.Name) || token.Token == "" || !utf8.ValidString(token.Token) || token.ID < 0 || token.ID >= e.VocabSize || names[token.Name] {
			return nil, ErrInvalidArgument
		}
		names[token.Name] = true
		if text, ok := ids[token.ID]; ok && text != token.Token {
			return nil, ErrInvalidArgument
		}
		ids[token.ID] = token.Token
	}
	seenEOS := map[int64]bool{}
	for _, id := range e.EOSIDs {
		if id < 0 || id >= e.VocabSize || seenEOS[id] {
			return nil, ErrInvalidArgument
		}
		if _, ok := ids[id]; !ok {
			return nil, ErrInvalidArgument
		}
		seenEOS[id] = true
	}
	sort.Slice(e.SpecialTokens, func(i, j int) bool { return e.SpecialTokens[i].Name < e.SpecialTokens[j].Name })
	sort.Slice(e.EOSIDs, func(i, j int) bool { return e.EOSIDs[i] < e.EOSIDs[j] })
	raw, err := encodeCanonicalJSON(e)
	if len(raw) > MaxManifestBytes {
		return nil, ErrInvalidArgument
	}
	return raw, err
}
func (e EffectiveTemplate) Digest() (string, error) { return contractDigest(e.Canonical()) }
func (e EffectiveTemplate) ValidateFor(plan TemplatePlan, capabilities RuntimeCapabilities) error {
	if err := plan.ValidateFor(capabilities); err != nil {
		return err
	}
	if _, err := e.Canonical(); err != nil {
		return err
	}
	planDigest, err := plan.Digest()
	if err != nil || e.TemplatePlanSHA256 != planDigest || e.RuntimeCapabilitiesSHA256 != plan.RuntimeCapabilitiesSHA256 || e.ImageDigest != plan.ImageDigest || e.SourceSHA256 != plan.SourceSHA256 {
		return ErrInvalidArgument
	}
	return nil
}
func ParseEffectiveTemplate(raw []byte) (EffectiveTemplate, error) {
	var e EffectiveTemplate
	if len(raw) == 0 || len(raw) > MaxManifestBytes || !validUnicodeEscapes(raw) || checkJSONValue(json.NewDecoder(bytes.NewReader(raw)), 0) != nil {
		return e, ErrInvalidArgument
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF {
		return e, ErrInvalidArgument
	}
	canonical, err := e.Canonical()
	if err != nil || !bytes.Equal(raw, canonical) {
		return EffectiveTemplate{}, ErrInvalidArgument
	}
	return e, nil
}
