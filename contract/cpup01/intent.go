// Package cpup01 owns the single active typed ModelDev execution contract.
package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidArgument = errors.New("INVALID_ARGUMENT")

const IntentSchemaVersion = "ani.modeldev.intent.v2.1"

type Parameter struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Intent struct {
	Name               string                `json:"name"`
	Kind               string                `json:"kind"`
	ImplementationID   string                `json:"implementation_id"`
	ComputePresetID    string                `json:"compute_preset_id"`
	DatasetVersionID   string                `json:"dataset_version_id"`
	MLP                *MLPParameters        `json:"-"`
	Finetuning         *FinetuningParameters `json:"-"`
	TimeoutSeconds     *uint64               `json:"timeout_seconds,omitempty"`
	BaseModelVersionID *string               `json:"base_model_version_id,omitempty"`
	TargetModelID      *string               `json:"target_model_id,omitempty"`
	NewModelName       *string               `json:"new_model_name,omitempty"`
}

type MLPParameters struct {
	LearningRate *string `json:"learning_rate,omitempty"`
	Epochs       *int64  `json:"epochs,omitempty"`
	BatchSize    *int64  `json:"batch_size,omitempty"`
	Seed         *uint32 `json:"seed,omitempty"`
}

type intentWire struct {
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	ImplementationID   string          `json:"implementation_id"`
	ComputePresetID    string          `json:"compute_preset_id"`
	DatasetVersionID   string          `json:"dataset_version_id"`
	Parameters         json.RawMessage `json:"parameters"`
	TimeoutSeconds     *uint64         `json:"timeout_seconds,omitempty"`
	BaseModelVersionID *string         `json:"base_model_version_id,omitempty"`
	TargetModelID      *string         `json:"target_model_id,omitempty"`
	NewModelName       *string         `json:"new_model_name,omitempty"`
}

func (i Intent) MarshalJSON() ([]byte, error) {
	var parameters any = i.MLP
	if i.Kind == "FINETUNING" {
		parameters = i.Finetuning
	}
	p, err := json.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	return json.Marshal(intentWire{Name: i.Name, Kind: i.Kind, ImplementationID: i.ImplementationID, ComputePresetID: i.ComputePresetID, DatasetVersionID: i.DatasetVersionID, Parameters: p, TimeoutSeconds: i.TimeoutSeconds, BaseModelVersionID: i.BaseModelVersionID, TargetModelID: i.TargetModelID, NewModelName: i.NewModelName})
}

func (i *Intent) UnmarshalJSON(raw []byte) error {
	parsed, err := parseIntent(raw)
	if err == nil {
		*i = parsed
	}
	return err
}
func ParseIntent(raw []byte) (Intent, error) { return parseIntent(raw) }

// DecodeCanonicalIntent is the sole stored-intent decoder. The schema marker
// is verified here and cannot be submitted in the user-facing request body.
func DecodeCanonicalIntent(raw []byte) (Intent, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Intent{}, ErrInvalidArgument
	}
	var schema string
	if json.Unmarshal(fields["schema"], &schema) != nil || schema != IntentSchemaVersion {
		return Intent{}, ErrInvalidArgument
	}
	delete(fields, "schema")
	body, err := encodeCanonicalJSON(fields)
	if err != nil {
		return Intent{}, err
	}
	intent, err := ParseIntent(body)
	if err != nil {
		return Intent{}, err
	}
	canonical, _, err := CanonicalIntent(intent)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Intent{}, ErrInvalidArgument
	}
	return intent, nil
}

func parseIntent(raw []byte) (Intent, error) {
	invalid := func() (Intent, error) { return Intent{}, fmt.Errorf("%w: intent JSON", ErrInvalidArgument) }
	if len(raw) == 0 || len(raw) > 16384 || !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return invalid()
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if checkJSONValue(scanner, 0) != nil {
		return invalid()
	}
	if _, err := scanner.Token(); err != io.EOF {
		return invalid()
	}
	var w intentWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil {
		return invalid()
	}
	if len(w.Parameters) == 0 || w.Parameters[0] != '{' {
		return invalid()
	}
	i := Intent{Name: w.Name, Kind: w.Kind, ImplementationID: w.ImplementationID, ComputePresetID: w.ComputePresetID, DatasetVersionID: w.DatasetVersionID, TimeoutSeconds: w.TimeoutSeconds, BaseModelVersionID: w.BaseModelVersionID, TargetModelID: w.TargetModelID, NewModelName: w.NewModelName}
	p := json.NewDecoder(bytes.NewReader(w.Parameters))
	p.DisallowUnknownFields()
	switch i.Kind {
	case "GENERAL_TRAINING":
		i.MLP = &MLPParameters{}
		if p.Decode(i.MLP) != nil {
			return invalid()
		}
	case "FINETUNING":
		i.Finetuning = &FinetuningParameters{}
		if p.Decode(i.Finetuning) != nil {
			return invalid()
		}
	default:
		return invalid()
	}
	if err := validateIntent(i); err != nil {
		return Intent{}, err
	}
	return i, nil
}

func CanonicalIntent(i Intent) ([]byte, string, error) {
	if err := validateIntent(i); err != nil {
		return nil, "", err
	}
	i.ComputePresetID = strings.ToLower(i.ComputePresetID)
	i.DatasetVersionID = strings.ToLower(i.DatasetVersionID)
	if i.BaseModelVersionID != nil {
		v := strings.ToLower(*i.BaseModelVersionID)
		i.BaseModelVersionID = &v
	}
	if i.TargetModelID != nil {
		v := strings.ToLower(*i.TargetModelID)
		i.TargetModelID = &v
	}
	if i.MLP != nil {
		p := *i.MLP
		if p.LearningRate != nil {
			v := canonicalDecimal(*p.LearningRate)
			p.LearningRate = &v
		}
		i.MLP = &p
	}
	if i.Finetuning != nil {
		p := *i.Finetuning
		if p.LearningRate != nil {
			v := canonicalDecimal(*p.LearningRate)
			p.LearningRate = &v
		}
		if p.LoraDropout != nil {
			v := canonicalDecimal(*p.LoraDropout)
			p.LoraDropout = &v
		}
		i.Finetuning = &p
	}
	raw, err := i.MarshalJSON()
	if err != nil {
		return nil, "", err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, "", ErrInvalidArgument
	}
	schema, _ := json.Marshal(IntentSchemaVersion)
	fields["schema"] = schema
	canonical, err := encodeCanonicalJSON(fields)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validUUID(v string) bool {
	return uuidPattern.MatchString(v) && v != "00000000-0000-0000-0000-000000000000"
}

func validateIntent(i Intent) error {
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, field) }
	if !utf8.ValidString(i.Name) || utf8.RuneCountInString(i.Name) < 1 || utf8.RuneCountInString(i.Name) > 80 || strings.TrimSpace(i.Name) != i.Name {
		return bad("name")
	}
	for _, r := range i.Name {
		if unicode.IsControl(r) {
			return bad("name")
		}
	}
	if !validUUID(i.ComputePresetID) || !validUUID(i.DatasetVersionID) {
		return bad("required identity")
	}
	if i.TimeoutSeconds != nil && *i.TimeoutSeconds == 0 {
		return bad("timeout_seconds")
	}
	switch i.Kind {
	case "GENERAL_TRAINING":
		if i.ImplementationID != "builtin.mlp" || i.MLP == nil || i.Finetuning != nil || i.BaseModelVersionID != nil || i.TargetModelID != nil || i.NewModelName != nil {
			return bad("implementation")
		}
	case "FINETUNING":
		if i.ImplementationID != "builtin.sft_lora" || i.Finetuning == nil || i.MLP != nil || i.BaseModelVersionID == nil || !validUUID(*i.BaseModelVersionID) || i.TargetModelID != nil && i.NewModelName != nil {
			return bad("implementation or model target")
		}
		if i.TargetModelID != nil && !validUUID(*i.TargetModelID) || i.NewModelName != nil && !validModelName(*i.NewModelName) {
			return bad("model target")
		}
	default:
		return bad("kind")
	}
	_, err := i.ExplicitParameters()
	return err
}

func checkJSONValue(d *json.Decoder, depth int) error {
	bad := func() error { return fmt.Errorf("%w: ambiguous JSON", ErrInvalidArgument) }
	if depth > 8 {
		return bad()
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return bad()
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			s, ok := key.(string)
			if err != nil || !ok || seen[s] {
				return bad()
			}
			seen[s] = true
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return bad()
		}
	case '[':
		for d.More() {
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

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
		v, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v < 0xd800 || v > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if e != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
