package biz

import "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"

// ResolveFinetuningTemplate is the shared read-only selection used by admission
// and readiness. The selected source is frozen; execution never reselects it.
func ResolveFinetuningTemplate(intent cpup01.Intent, base cpup01.BaseModelRef, material cpup01.ImplementationMaterial) (cpup01.TemplatePlan, error) {
	if material.Finetuning == nil {
		return cpup01.TemplatePlan{}, ErrFinetuningNotEnabled
	}
	fine := material.Finetuning
	c := fine.RuntimeCapabilities
	digest, err := c.Digest()
	if err != nil {
		return cpup01.TemplatePlan{}, ErrModelDependencyUnavailable
	}
	plan := cpup01.TemplatePlan{SchemaVersion: cpup01.TemplatePlanSchemaVersion, RuntimeCapabilitiesSHA256: digest, ImageDigest: c.ImageDigest}
	named := func(id, source string) (cpup01.TemplatePlan, error) {
		for _, t := range c.RegisteredTemplates {
			if t.ID == id {
				plan.Mode = "NAMED"
				plan.SelectionSource = source
				copyID := id
				plan.TemplateID = &copyID
				plan.SourceSHA256 = t.SourceSHA256
				return plan, plan.ValidateFor(c)
			}
		}
		return cpup01.TemplatePlan{}, ErrFinetuningTemplateUnsupported
	}
	if intent.FinetuningTemplate != nil {
		return named(*intent.FinetuningTemplate, "USER")
	}
	association := ""
	for _, a := range fine.ModelTemplateAssociations {
		if a.ModelVersionID == base.VersionID {
			if association != "" {
				return cpup01.TemplatePlan{}, ErrModelDependencyUnavailable
			}
			association = a.TemplateID
		}
	}
	if association != "" {
		return named(association, "MODEL_ASSOCIATION")
	}
	// Transformers' independent default file takes precedence over embedded
	// tokenizer config. Named alternatives require a deterministic default.
	var files, embedded []cpup01.TemplateSource
	for _, source := range base.Facts.TemplateSources {
		if source.Validate() != nil {
			return cpup01.TemplatePlan{}, ErrBaseModelInvalid
		}
		if source.Kind == "FILE" {
			if source.Path == "chat_template.jinja" {
				files = []cpup01.TemplateSource{source}
				break
			}
			files = append(files, source)
		}
	}
	if len(files) == 0 {
		for _, s := range base.Facts.TemplateSources {
			if s.Kind == "EMBEDDED" {
				embedded = append(embedded, s)
			}
		}
	}
	candidates := files
	if len(candidates) == 0 {
		candidates = embedded
	}
	var selected *cpup01.TemplateSource
	for _, source := range candidates {
		if source.Default {
			if selected != nil {
				return cpup01.TemplatePlan{}, ErrFinetuningTemplateRequired
			}
			copySource := source
			selected = &copySource
		}
	}
	if selected == nil {
		return cpup01.TemplatePlan{}, ErrFinetuningTemplateRequired
	}
	plan.Mode = "TOKENIZER_NATIVE"
	plan.SelectionSource = "MODEL_TOKENIZER"
	plan.Source = selected
	plan.SourceSHA256 = selected.SHA256
	if plan.ValidateFor(c) != nil {
		return cpup01.TemplatePlan{}, ErrFinetuningTemplateUnsupported
	}
	return plan, nil
}
