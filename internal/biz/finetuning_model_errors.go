package biz

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrBaseModelInvalid              = errors.New("BASE_MODEL_INVALID")
	ErrBaseModelUnsupported          = errors.New("BASE_MODEL_UNSUPPORTED")
	ErrContextLimitUnknown           = errors.New("CONTEXT_LIMIT_UNKNOWN")
	ErrContextLimitExceeded          = errors.New("CONTEXT_LIMIT_EXCEEDED")
	ErrFinetuningTemplateRequired    = errors.New("FINETUNING_TEMPLATE_REQUIRED")
	ErrFinetuningTemplateUnsupported = errors.New("FINETUNING_TEMPLATE_UNSUPPORTED")
	ErrModelTargetConflict           = errors.New("MODEL_NAME_CONFLICT")
	ErrModelSourceNotFound           = errors.New("MODEL_SOURCE_NOT_FOUND")
	ErrModelSourceForbidden          = errors.New("MODEL_SOURCE_FORBIDDEN")
)

// FinetuningSourceFactsReader reads one visible immutable source without any
// registration target. Readiness never needs a fabricated full training Intent.
type FinetuningSourceFactsReader interface {
	ResolveFinetuningSourceFacts(context.Context, string, string, cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error)
}
