// Package finetuningmodel loads explicitly pinned base-model facts used as a
// read-only Model boundary substitute for isolated FINETUNING candidacy.
//
// Loading these documents is never evidence of a Model permission, approval,
// protected status or environment readiness. They exist only so an isolated
// admission can freeze a base-model byte inventory without a live Model
// dependency. A production composition must replace this with the real
// Model read boundary.
package finetuningmodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

const (
	schemaVersion = "ani.modeldev.controlled-model-facts.v1"
	maxFiles      = 64
	maxFileBytes  = 64 * 1024
	maxJSONDepth  = 16
)

var (
	ErrInvalidModelFacts     = errors.New("INVALID_CONTROLLED_MODEL_FACTS")
	ErrModelFactsUnavailable = errors.New("CONTROLLED_MODEL_FACTS_UNAVAILABLE")
)

// FileSource comes only from trusted startup configuration. SHA256 identifies
// the actual file bytes; it is neither a tenant grant nor an approval record.
type FileSource struct {
	Path   string
	SHA256 string
}

// document is the private pinned file format. It carries only the frozen base
// model reference the admission must copy into the Snapshot.
type document struct {
	SchemaVersion string              `json:"schema_version"`
	TenantID      string              `json:"tenant_id"`
	VersionID     string              `json:"version_id"`
	BaseModel     cpup01.BaseModelRef `json:"base_model"`
}

// Reader owns immutable loaded facts keyed by resource tenant and base version.
// It never performs a network call and never mutates the Model service.
type Reader struct {
	documents map[documentKey]document
}

type documentKey struct {
	tenantID  string
	versionID string
}

var _ biz.FinetuningModelFactsReader = (*Reader)(nil)

// Load validates every pinned document before any admission can use it.
func Load(ctx context.Context, sources []FileSource) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(sources) == 0 || len(sources) > maxFiles {
		return nil, ErrInvalidModelFacts
	}
	documents := make(map[documentKey]document, len(sources))
	for _, source := range sources {
		value, err := readDocument(ctx, source)
		if err != nil {
			return nil, err
		}
		key := documentKey{strings.ToLower(value.TenantID), strings.ToLower(value.VersionID)}
		if _, exists := documents[key]; exists {
			return nil, ErrInvalidModelFacts
		}
		documents[key] = value
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Reader{documents: documents}, nil
}

// ResolveFinetuningModelFacts returns the frozen base-model reference for the
// requested tenant and fixed version. A missing or mismatched key is a finite
// domain error, never a fabricated success.
func (reader *Reader) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, release cpup01.ReleaseDocument) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if reader == nil || intent.BaseModelVersionID == nil || release.Finetuning == nil {
		return cpup01.BaseModelRef{}, biz.ErrFinetuningNotEnabled
	}
	tenant = strings.ToLower(tenant)
	version := strings.ToLower(*intent.BaseModelVersionID)
	if !canonicalID(tenant) || !canonicalID(version) || version != strings.ToLower(release.Finetuning.BaseModelVersionID) {
		return cpup01.BaseModelRef{}, biz.ErrNoCompatibleRelease
	}
	value, found := reader.documents[documentKey{tenant, version}]
	if !found {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	base := value.BaseModel
	base.Files = append([]cpup01.BaseModelFile{}, base.Files...)
	return base, nil
}

func readDocument(ctx context.Context, source FileSource) (document, error) {
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	if !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path || strings.ContainsRune(source.Path, 0) || !validDigest(source.SHA256) {
		return document{}, ErrInvalidModelFacts
	}
	// The pathname is supplied only by trusted startup configuration. Avoid
	// blocking on special files and do not follow a final-component symlink.
	fd, err := syscall.Open(source.Path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return document{}, ErrModelFactsUnavailable
	}
	file := os.NewFile(uintptr(fd), source.Path)
	defer file.Close()
	var before syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > maxFileBytes {
		return document{}, ErrInvalidModelFacts
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	if err != nil {
		return document{}, ErrModelFactsUnavailable
	}
	var after syscall.Stat_t
	if syscall.Fstat(fd, &after) != nil || int64(len(raw)) != before.Size || after.Size != before.Size || after.Nlink != 1 || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return document{}, ErrInvalidModelFacts
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != source.SHA256 || !utf8.Valid(raw) {
		return document{}, ErrInvalidModelFacts
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	if validJSONValue(tokens, 0) != nil {
		return document{}, ErrInvalidModelFacts
	}
	if _, err := tokens.Token(); err != io.EOF {
		return document{}, ErrInvalidModelFacts
	}
	var value document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || !hasRequiredDocumentFields(raw, reflect.TypeOf(value)) ||
		value.SchemaVersion != schemaVersion || !canonicalID(value.TenantID) || !canonicalID(value.VersionID) ||
		value.BaseModel.VersionID != value.VersionID {
		return document{}, ErrInvalidModelFacts
	}
	if err := ctx.Err(); err != nil {
		return document{}, err
	}
	return value, nil
}

// validJSONValue rejects duplicate keys, nulls and excessive nesting before the
// typed decoder enforces the private field set.
func validJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return ErrInvalidModelFacts
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delim := token.(type) {
	case json.Delim:
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrInvalidModelFacts
				}
				seen[name] = true
				if err := validJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalidModelFacts
			}
		case '[':
			for decoder.More() {
				if err := validJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalidModelFacts
			}
		default:
			return ErrInvalidModelFacts
		}
	case nil:
		return ErrInvalidModelFacts
	}
	return nil
}

// hasRequiredDocumentFields requires every tagged field without an omitempty
// option to be present, so an omitted scalar cannot silently default to a
// weaker value while optional fixed-object markers remain absent.
func hasRequiredDocumentFields(raw []byte, shape reflect.Type) bool {
	if shape.Kind() != reflect.Struct {
		return true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	for index := 0; index < shape.NumField(); index++ {
		field := shape.Field(index)
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return false
		}
		value, present := object[name]
		if !present {
			if strings.Contains(options, "omitempty") {
				continue
			}
			return false
		}
		if !hasRequiredDocumentFields(value, field.Type) {
			return false
		}
	}
	return true
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
