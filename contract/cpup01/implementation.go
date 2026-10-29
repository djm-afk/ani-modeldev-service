package cpup01

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

//go:embed implementations.json
var implementationDefinitions []byte

const MLPParameterContractVersion = "ani.mlp.parameters.v3"
const FinetuningParameterContractVersion = "ani.sft-lora.parameters.v3"
const FinetuningIntentSchemaVersion = IntentSchemaVersion

type FinetuningParameters struct {
	LearningRate              *string `json:"learning_rate,omitempty"`
	MaxSteps                  *int64  `json:"max_steps,omitempty"`
	PerDeviceTrainBatchSize   *int64  `json:"per_device_train_batch_size,omitempty"`
	GradientAccumulationSteps *int64  `json:"gradient_accumulation_steps,omitempty"`
	CutoffLen                 *int64  `json:"cutoff_len,omitempty"`
	LoraRank                  *int64  `json:"lora_rank,omitempty"`
	LoraAlpha                 *int64  `json:"lora_alpha,omitempty"`
	LoraDropout               *string `json:"lora_dropout,omitempty"`
	Seed                      *uint32 `json:"seed,omitempty"`
}

type ImplementationDefinition struct {
	Kind                     string                     `json:"kind"`
	Version                  string                     `json:"version"`
	ParameterContractVersion string                     `json:"parameter_contract_version"`
	Defaults                 map[string]json.RawMessage `json:"defaults"`
	IntegerMax               string                     `json:"integer_max"`
	SeedMax                  string                     `json:"seed_max"`
	DecimalMaxCharacters     int                        `json:"decimal_max_characters"`
	LearningRateDtype        string                     `json:"learning_rate_dtype"`
}

func Implementations() map[string]ImplementationDefinition {
	var root struct {
		Implementations map[string]ImplementationDefinition `json:"implementations"`
	}
	if json.Unmarshal(implementationDefinitions, &root) != nil {
		panic("invalid compiled implementation definitions")
	}
	return root.Implementations
}

func (i Intent) ExplicitParameters() ([]Parameter, error) {
	var raw []byte
	var err error
	switch i.Kind {
	case "GENERAL_TRAINING":
		if i.MLP == nil {
			return nil, ErrInvalidArgument
		}
		raw, err = json.Marshal(i.MLP)
	case "FINETUNING":
		if i.Finetuning == nil {
			return nil, ErrInvalidArgument
		}
		raw, err = json.Marshal(i.Finetuning)
	default:
		return nil, ErrInvalidArgument
	}
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrInvalidArgument
	}
	return parameterFields(i.ImplementationID, fields)
}

func parameterFields(implementation string, fields map[string]json.RawMessage) ([]Parameter, error) {
	definition, ok := Implementations()[implementation]
	if !ok {
		return nil, ErrInvalidArgument
	}
	parameters := make([]Parameter, 0, len(fields))
	for name, raw := range fields {
		if _, ok := definition.Defaults[name]; !ok {
			return nil, fmt.Errorf("%w: unknown parameter %s", ErrInvalidArgument, name)
		}
		p := Parameter{Name: name, Type: "INTEGER"}
		switch name {
		case "learning_rate", "lora_dropout":
			p.Type = "DECIMAL"
			if json.Unmarshal(raw, &p.Value) != nil {
				return nil, ErrInvalidArgument
			}
		default:
			p.Value = string(raw)
		}
		if validateImplementationParameter(implementation, p) != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidArgument, name)
		}
		parameters = append(parameters, p)
	}
	return canonicalParameters(parameters), nil
}

func (i Intent) ResolveParameters() ([]Parameter, error) {
	definition, ok := Implementations()[i.ImplementationID]
	if !ok || definition.Kind != i.Kind {
		return nil, ErrInvalidArgument
	}
	parameters, err := parameterFields(i.ImplementationID, definition.Defaults)
	if err != nil {
		return nil, err
	}
	explicit, err := i.ExplicitParameters()
	if err != nil {
		return nil, err
	}
	for _, p := range explicit {
		for n := range parameters {
			if parameters[n].Name == p.Name {
				parameters[n] = p
				break
			}
		}
	}
	return canonicalParameters(parameters), nil
}

func validateImplementationParameter(implementation string, p Parameter) error {
	definition, ok := Implementations()[implementation]
	if !ok {
		return ErrInvalidArgument
	}
	if _, ok := definition.Defaults[p.Name]; !ok {
		return ErrInvalidArgument
	}
	switch p.Name {
	case "learning_rate", "lora_dropout":
		if p.Type != "DECIMAL" || len(p.Value) > definition.DecimalMaxCharacters || !decimalPattern.MatchString(p.Value) {
			return ErrInvalidArgument
		}
		bits := 32
		if p.Name == "lora_dropout" {
			bits = 64
		}
		v, err := strconv.ParseFloat(p.Value, bits)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return ErrInvalidArgument
		}
		if p.Name == "learning_rate" && v <= 0 || p.Name == "lora_dropout" && (v < 0 || v >= 1) {
			return ErrInvalidArgument
		}
	case "seed":
		v, err := strconv.ParseUint(p.Value, 10, 32)
		if p.Type != "INTEGER" || err != nil || strconv.FormatUint(v, 10) != p.Value {
			return ErrInvalidArgument
		}
	default:
		v, err := strconv.ParseInt(p.Value, 10, 64)
		if p.Type != "INTEGER" || err != nil || v < 1 || strconv.FormatInt(v, 10) != p.Value {
			return ErrInvalidArgument
		}
	}
	return nil
}

func ValidateResolvedParameters(implementation string, parameters []Parameter) error {
	definition, ok := Implementations()[implementation]
	if !ok || len(parameters) != len(definition.Defaults) {
		return ErrInvalidArgument
	}
	seen := map[string]bool{}
	for _, p := range parameters {
		if seen[p.Name] || validateImplementationParameter(implementation, p) != nil {
			return ErrInvalidArgument
		}
		seen[p.Name] = true
	}
	return nil
}

func validateFinetuningParameter(p Parameter) error {
	return validateImplementationParameter("builtin.sft_lora", p)
}
func validateCompleteFinetuningParameters(p []Parameter) error {
	return ValidateResolvedParameters("builtin.sft_lora", p)
}
func ParseFinetuningIntent(raw []byte) (Intent, error) {
	i, e := ParseIntent(raw)
	if e == nil && i.Kind != "FINETUNING" {
		return Intent{}, ErrInvalidArgument
	}
	return i, e
}
func canonicalDecimal(v string) string {
	for len(v) > 0 && v[len(v)-1] == '0' {
		found := false
		for _, c := range v {
			if c == '.' {
				found = true
			}
		}
		if !found {
			break
		}
		v = v[:len(v)-1]
	}
	if len(v) > 0 && v[len(v)-1] == '.' {
		v = v[:len(v)-1]
	}
	return v
}
func validModelName(v string) bool {
	if len(v) == 0 || len(v) > 63 {
		return false
	}
	for _, r := range v {
		if !(r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
