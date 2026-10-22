// Package cpup01 defines the versioned CPU-P01 admission contract shared by
// Governance and ModelDev. It contains no transport, persistence or runtime.
package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidArgument = errors.New("INVALID_ARGUMENT")

// Parameter is an explicitly supplied, typed registered-program parameter.
// Value uses a decimal string even for integer values; it never contains JSON.
type Parameter struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Intent preserves optional-field presence before any Release defaults resolve.
// Tenant, actor and idempotency key belong to the trusted admission scope, not
// the intent. In particular this object cannot select a namespace or command.
type Intent struct {
	Name               string                `json:"name"`
	Kind               string                `json:"kind"`
	PresetID           string                `json:"preset_id"`
	DatasetVersionID   string                `json:"dataset_version_id"`
	ImageVersionID     *string               `json:"image_version_id,omitempty"`
	GeneralParameters  *[]Parameter          `json:"general_parameters,omitempty"`
	SourceExecutionID  *string               `json:"source_execution_id,omitempty"`
	BaseModelVersionID *string               `json:"base_model_version_id,omitempty"`
	Finetuning         *FinetuningParameters `json:"finetuning,omitempty"`
	TargetModelID      *string               `json:"target_model_id,omitempty"`
	NewModelName       *string               `json:"new_model_name,omitempty"`
}

// ParseIntent rejects ambiguous JSON before computing an admission hash.
func ParseIntent(raw []byte) (Intent, error) {
	if len(raw) > 16384 || !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return Intent{}, fmt.Errorf("%w: intent encoding or size", ErrInvalidArgument)
	}
	var selector struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &selector); err == nil && selector.Kind == "FINETUNING" {
		return ParseFinetuningIntent(raw)
	}
	if len(raw) > 16384 || !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return Intent{}, fmt.Errorf("%w: intent encoding or size", ErrInvalidArgument)
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	if err := checkJSONValue(scanner, 0); err != nil {
		return Intent{}, err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return Intent{}, fmt.Errorf("%w: trailing JSON", ErrInvalidArgument)
	}
	var intent Intent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return Intent{}, fmt.Errorf("%w: intent JSON", ErrInvalidArgument)
	}
	if err := validateIntent(intent); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

// CanonicalIntent returns the versioned, deterministic intent JSON and its
// lowercase SHA256. It does not resolve or read a current Release.
func CanonicalIntent(intent Intent) ([]byte, string, error) {
	if err := validateIntent(intent); err != nil {
		return nil, "", err
	}
	intent.PresetID = strings.ToLower(intent.PresetID)
	intent.DatasetVersionID = strings.ToLower(intent.DatasetVersionID)
	if intent.ImageVersionID != nil {
		value := strings.ToLower(*intent.ImageVersionID)
		intent.ImageVersionID = &value
	}
	if intent.SourceExecutionID != nil {
		value := strings.ToLower(*intent.SourceExecutionID)
		intent.SourceExecutionID = &value
	}
	if intent.BaseModelVersionID != nil {
		value := strings.ToLower(*intent.BaseModelVersionID)
		intent.BaseModelVersionID = &value
	}
	if intent.TargetModelID != nil {
		value := strings.ToLower(*intent.TargetModelID)
		intent.TargetModelID = &value
	}
	if intent.Finetuning != nil {
		parameters := *intent.Finetuning
		if parameters.LearningRate != nil {
			value := canonicalDecimal(*parameters.LearningRate)
			parameters.LearningRate = &value
		}
		intent.Finetuning = &parameters
	}
	if intent.GeneralParameters != nil {
		parameters := canonicalParameters(*intent.GeneralParameters)
		intent.GeneralParameters = &parameters
	}
	value := struct {
		Schema string `json:"schema"`
		Intent
	}{Schema: intentSchema(intent.Kind), Intent: intent}
	canonical, err := encodeCanonicalJSON(value)
	if err != nil {
		return nil, "", fmt.Errorf("%w: intent encoding", ErrInvalidArgument)
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validUUID(value string) bool {
	return uuidPattern.MatchString(value) && value != "00000000-0000-0000-0000-000000000000"
}

func validateIntent(intent Intent) error {
	invalid := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, field) }
	if !utf8.ValidString(intent.Name) || utf8.RuneCountInString(intent.Name) < 1 || utf8.RuneCountInString(intent.Name) > 80 || strings.TrimSpace(intent.Name) != intent.Name {
		return invalid("name")
	}
	for _, r := range intent.Name {
		if unicode.IsControl(r) {
			return invalid("name")
		}
	}
	if intent.Kind == "FINETUNING" {
		return validateFinetuningIntent(intent)
	}
	if intent.Kind != "GENERAL_TRAINING" {
		return invalid("kind")
	}
	if intent.Finetuning != nil || intent.BaseModelVersionID != nil || intent.TargetModelID != nil || intent.NewModelName != nil {
		return invalid("kind")
	}
	if !validUUID(intent.PresetID) || !validUUID(intent.DatasetVersionID) {
		return invalid("required version ID")
	}
	if intent.ImageVersionID != nil && !validUUID(*intent.ImageVersionID) {
		return invalid("image_version_id")
	}
	if intent.SourceExecutionID != nil && !validUUID(*intent.SourceExecutionID) {
		return invalid("source_execution_id")
	}
	if intent.GeneralParameters == nil {
		return nil
	}
	return validateRegisteredParameters(*intent.GeneralParameters)
}

func validateRegisteredParameters(parameters []Parameter) error {
	invalid := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, field) }
	if len(parameters) > 3 {
		return invalid("general_parameters")
	}
	seen := make(map[string]bool)
	for _, parameter := range parameters {
		if seen[parameter.Name] {
			return invalid("duplicate parameter")
		}
		seen[parameter.Name] = true
		switch parameter.Name {
		case "epochs":
			if parameter.Type != "INTEGER" || parameter.Value != "3" {
				return invalid("epochs")
			}
		case "batch_size":
			if parameter.Type != "INTEGER" || parameter.Value != "64" {
				return invalid("batch_size")
			}
		case "learning_rate":
			if parameter.Type != "DECIMAL" || len(parameter.Value) > 32 || !decimalPattern.MatchString(parameter.Value) {
				return invalid("learning_rate")
			}
			value, ok := new(big.Rat).SetString(parameter.Value)
			if !ok || value.Sign() <= 0 || value.Cmp(big.NewRat(1, 10)) > 0 {
				return invalid("learning_rate")
			}
		default:
			return invalid("unregistered parameter")
		}
	}
	return nil
}

// encoding/json alone accepts duplicate keys and nulls. A bounded token pass
// rejects those ambiguities before the ordinary typed decoder handles shape.
func checkJSONValue(decoder *json.Decoder, depth int) error {
	invalid := func() error { return fmt.Errorf("%w: ambiguous JSON", ErrInvalidArgument) }
	if depth > 8 {
		return invalid()
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return invalid()
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return invalid()
			}
			if !validIntentKey(depth, key) {
				return invalid()
			}
			seen[key] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return invalid()
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func validIntentKey(depth int, key string) bool {
	if depth == 0 {
		switch key {
		case "name", "kind", "preset_id", "dataset_version_id", "image_version_id", "general_parameters", "source_execution_id":
			return true
		}
	}
	return depth == 2 && (key == "name" || key == "type" || key == "value")
}

// Go's JSON decoder replaces unpaired UTF-16 escapes with U+FFFD. Reject them
// so malformed input cannot acquire the identity of a different valid string.
func validUnicodeEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
