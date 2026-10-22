package cpup01

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

const FinetuningParameterContractVersion = "ani.finetune.lora.parameters.v1"
const FinetuningIntentSchemaVersion = "ani.modeldev.finetuning-intent.v1"

// FinetuningParameters preserves each explicit field. Defaults are resolved
// only for a new admission; a present empty object is a valid original intent.
type FinetuningParameters struct {
	LearningRate *string `json:"learning_rate,omitempty"`
	MaxSteps     *int64  `json:"max_steps,omitempty"`
}

// ParameterRule is a bounded Release rule, never an expression or framework
// option map. Exactly one nonempty enumeration or numeric range is present.
type ParameterRule struct {
	Name             string      `json:"name"`
	Type             string      `json:"type"`
	Required         bool        `json:"required"`
	AllowedValues    []Parameter `json:"allowed_values,omitempty"`
	Minimum          *Parameter  `json:"minimum,omitempty"`
	Maximum          *Parameter  `json:"maximum,omitempty"`
	MinimumInclusive *bool       `json:"minimum_inclusive,omitempty"`
	MaximumInclusive *bool       `json:"maximum_inclusive,omitempty"`
}

func intentSchema(kind string) string {
	if kind == "FINETUNING" {
		return FinetuningIntentSchemaVersion
	}
	return "ani.modeldev.intent.v1"
}

// ParseFinetuningIntent selects a fixed request codec without reading a
// mutable Release. It checks shape and numeric spelling before original-key
// lookup; program bounds and approved combinations are new-admission checks.
func ParseFinetuningIntent(raw []byte) (Intent, error) {
	invalid := func(reason string) (Intent, error) { return Intent{}, fmt.Errorf("%w: %s", ErrInvalidArgument, reason) }
	if len(raw) == 0 || len(raw) > 16384 || !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return invalid("intent encoding or size")
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := checkFinetuningJSON(scanner, 0); err != nil {
		return Intent{}, err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return invalid("trailing JSON")
	}
	var intent Intent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return invalid("intent JSON")
	}
	if err := validateIntent(intent); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func checkFinetuningJSON(decoder *json.Decoder, depth int) error {
	invalid := func() error { return fmt.Errorf("%w: ambiguous JSON", ErrInvalidArgument) }
	if depth > 2 {
		return invalid()
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return invalid()
	}
	if delimiter, ok := token.(json.Delim); ok {
		if delimiter != '{' {
			return invalid()
		}
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] || !validFinetuningKey(depth, key) {
				return invalid()
			}
			seen[key] = true
			if err := checkFinetuningJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return invalid()
		}
	}
	return nil
}

func validFinetuningKey(depth int, key string) bool {
	if depth == 0 {
		switch key {
		case "name", "kind", "preset_id", "dataset_version_id", "base_model_version_id", "finetuning", "target_model_id", "new_model_name":
			return true
		}
	}
	return depth == 1 && (key == "learning_rate" || key == "max_steps")
}

func validateFinetuningIntent(intent Intent) error {
	invalid := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, field) }
	if !validUUID(intent.PresetID) || !validUUID(intent.DatasetVersionID) {
		return invalid("required version ID")
	}
	if intent.ImageVersionID != nil || intent.GeneralParameters != nil || intent.SourceExecutionID != nil {
		return invalid("finetuning fields")
	}
	if intent.BaseModelVersionID == nil || !validUUID(*intent.BaseModelVersionID) {
		return invalid("base_model_version_id")
	}
	if intent.Finetuning == nil {
		return invalid("finetuning")
	}
	if (intent.TargetModelID == nil) == (intent.NewModelName == nil) {
		return invalid("model target")
	}
	if intent.TargetModelID != nil && !validUUID(*intent.TargetModelID) {
		return invalid("target_model_id")
	}
	if intent.NewModelName != nil && !validModelName(*intent.NewModelName) {
		return invalid("new_model_name")
	}
	if value := intent.Finetuning.LearningRate; value != nil && (len(*value) > 32 || !decimalPattern.MatchString(*value)) {
		return invalid("learning_rate")
	}
	return nil
}

// This is the published Model create-only name grammar, verified against the
// provider. Current permissions/name uniqueness remain the provider's facts.
func validModelName(value string) bool {
	if len(value) == 0 || len(value) > 63 || strings.Trim(value, " ") != value {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func canonicalDecimal(value string) string {
	if strings.Contains(value, ".") {
		return strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	return value
}

func validateFinetuningParameter(parameter Parameter) error {
	invalid := func() error { return fmt.Errorf("%w: %s", ErrInvalidArgument, parameter.Name) }
	switch parameter.Name {
	case "learning_rate":
		if parameter.Type != "DECIMAL" || len(parameter.Value) > 32 || !decimalPattern.MatchString(parameter.Value) {
			return invalid()
		}
		value, ok := new(big.Rat).SetString(parameter.Value)
		if !ok || value.Sign() <= 0 || value.Cmp(big.NewRat(1, 1)) > 0 {
			return invalid()
		}
	case "max_steps":
		value, err := strconv.ParseInt(parameter.Value, 10, 64)
		if parameter.Type != "INTEGER" || err != nil || strconv.FormatInt(value, 10) != parameter.Value || value < 1 || value > 200 {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func validateCompleteFinetuningParameters(parameters []Parameter) error {
	if len(parameters) != 2 {
		return fmt.Errorf("%w: complete finetuning parameters", ErrInvalidArgument)
	}
	seen := map[string]bool{}
	for _, parameter := range parameters {
		if seen[parameter.Name] {
			return fmt.Errorf("%w: duplicate parameter", ErrInvalidArgument)
		}
		seen[parameter.Name] = true
		if err := validateFinetuningParameter(parameter); err != nil {
			return err
		}
	}
	return nil
}

// ResolveFinetuningParameters checks the immutable Release and applies only
// the requested fields. A partial candidate never changes another parameter
// to find an approved combination.
func ResolveFinetuningParameters(program ReleaseProgram, explicit FinetuningParameters) ([]Parameter, error) {
	if err := ValidateFinetuningProgramParameters(program); err != nil {
		return nil, err
	}
	parameters := append([]Parameter{}, program.DefaultParameters...)
	for i := range parameters {
		switch parameters[i].Name {
		case "learning_rate":
			if explicit.LearningRate != nil {
				parameters[i].Value = *explicit.LearningRate
			}
		case "max_steps":
			if explicit.MaxSteps != nil {
				parameters[i].Value = strconv.FormatInt(*explicit.MaxSteps, 10)
			}
		}
	}
	if err := validateCompleteFinetuningParameters(parameters); err != nil {
		return nil, err
	}
	for _, parameter := range parameters {
		for _, rule := range program.ParameterRules {
			if rule.Name == parameter.Name && !parameterAllowed(rule, parameter) {
				return nil, fmt.Errorf("%w: %s not approved", ErrInvalidArgument, parameter.Name)
			}
		}
	}
	parameters = canonicalParameters(parameters)
	for _, set := range program.AllowedParameterSets {
		if sameParameters(parameters, set) {
			return parameters, nil
		}
	}
	return nil, fmt.Errorf("%w: parameter combination not approved", ErrInvalidArgument)
}

// ValidateFinetuningProgramParameters verifies the two-field existence
// contract as well as each approved complete combination. This cannot approve
// a candidate: publication still requires the actual workload evidence.
func ValidateFinetuningProgramParameters(program ReleaseProgram) error {
	invalid := func() error { return fmt.Errorf("%w: finetuning parameter rules", ErrInvalidArgument) }
	if program.ParameterContractVersion != FinetuningParameterContractVersion || validateCompleteFinetuningParameters(program.DefaultParameters) != nil || len(program.ParameterRules) != 2 || len(program.AllowedParameterSets) == 0 || len(program.AllowedParameterSets) > 64 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, rule := range program.ParameterRules {
		if seen[rule.Name] || rule.Required {
			return invalid()
		}
		seen[rule.Name] = true
		probe := Parameter{Name: rule.Name, Type: rule.Type, Value: "1"}
		if validateFinetuningParameter(probe) != nil {
			return invalid()
		}
		if len(rule.AllowedValues) > 0 {
			if len(rule.AllowedValues) > 64 || rule.Minimum != nil || rule.Maximum != nil || rule.MinimumInclusive != nil || rule.MaximumInclusive != nil {
				return invalid()
			}
			values := map[string]bool{}
			for _, value := range rule.AllowedValues {
				if value.Name != rule.Name || value.Type != rule.Type || validateFinetuningParameter(value) != nil || values[canonicalDecimal(value.Value)] {
					return invalid()
				}
				values[canonicalDecimal(value.Value)] = true
			}
		} else {
			if rule.Minimum == nil || rule.Maximum == nil || rule.MinimumInclusive == nil || rule.MaximumInclusive == nil {
				return invalid()
			}
			for index, value := range []Parameter{*rule.Minimum, *rule.Maximum} {
				boundaryZero := index == 0 && rule.Name == "learning_rate" && value.Type == "DECIMAL" && decimalPattern.MatchString(value.Value) && canonicalDecimal(value.Value) == "0" && !*rule.MinimumInclusive
				if value.Name != rule.Name || value.Type != rule.Type || (!boundaryZero && validateFinetuningParameter(value) != nil) {
					return invalid()
				}
			}
			minimum, _ := new(big.Rat).SetString(rule.Minimum.Value)
			maximum, _ := new(big.Rat).SetString(rule.Maximum.Value)
			if minimum.Cmp(maximum) > 0 || (minimum.Cmp(maximum) == 0 && (!*rule.MinimumInclusive || !*rule.MaximumInclusive)) {
				return invalid()
			}
		}
	}
	sets := map[string]bool{}
	defaultApproved := false
	for _, set := range program.AllowedParameterSets {
		if validateCompleteFinetuningParameters(set) != nil {
			return invalid()
		}
		for _, parameter := range set {
			for _, rule := range program.ParameterRules {
				if rule.Name == parameter.Name && !parameterAllowed(rule, parameter) {
					return invalid()
				}
			}
		}
		key, _ := encodeCanonicalJSON(canonicalParameters(set))
		if sets[string(key)] {
			return invalid()
		}
		sets[string(key)] = true
		if sameParameters(set, program.DefaultParameters) {
			defaultApproved = true
		}
	}
	if !defaultApproved {
		return invalid()
	}
	return nil
}

func parameterAllowed(rule ParameterRule, parameter Parameter) bool {
	if rule.Name != parameter.Name || rule.Type != parameter.Type {
		return false
	}
	value, ok := new(big.Rat).SetString(parameter.Value)
	if !ok {
		return false
	}
	for _, candidate := range rule.AllowedValues {
		candidateValue, ok := new(big.Rat).SetString(candidate.Value)
		if ok && value.Cmp(candidateValue) == 0 {
			return true
		}
	}
	if len(rule.AllowedValues) > 0 || rule.Minimum == nil || rule.Maximum == nil || rule.MinimumInclusive == nil || rule.MaximumInclusive == nil {
		return false
	}
	minimum, minOK := new(big.Rat).SetString(rule.Minimum.Value)
	maximum, maxOK := new(big.Rat).SetString(rule.Maximum.Value)
	if !minOK || !maxOK {
		return false
	}
	lo, hi := value.Cmp(minimum), value.Cmp(maximum)
	return (lo > 0 || lo == 0 && *rule.MinimumInclusive) && (hi < 0 || hi == 0 && *rule.MaximumInclusive)
}

func sameParameters(left, right []Parameter) bool {
	a, b := canonicalParameters(left), canonicalParameters(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
