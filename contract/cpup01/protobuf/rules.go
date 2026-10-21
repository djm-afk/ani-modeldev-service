package protobuf

import (
	"encoding/json"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"sort"
)

func EncodeImplementationParameterRules(implementationID string) ([]*modeldevv1.ParameterRule, error) {
	definition, ok := cpup01.Implementations()[implementationID]
	if !ok {
		return nil, cpup01.ErrInvalidArgument
	}
	keys := make([]string, 0, len(definition.Defaults))
	for k := range definition.Defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]*modeldevv1.ParameterRule, 0, len(keys))
	for _, name := range keys {
		p := cpup01.Parameter{Name: name, Type: "INTEGER", Value: string(definition.Defaults[name])}
		minimum := cpup01.Parameter{Name: name, Type: "INTEGER", Value: "1"}
		maximum := cpup01.Parameter{Name: name, Type: "INTEGER", Value: definition.IntegerMax}
		minimumInclusive, maximumInclusive := true, true
		rule := &modeldevv1.ParameterRule{Name: name, Type: modeldevv1.ParameterType_PARAMETER_TYPE_INTEGER, Editable: true, NumericRepresentation: "int64", ValidationDescription: "positive integer supported by the pinned Python/PyTorch framework"}
		switch name {
		case "learning_rate", "lora_dropout":
			p.Type = "DECIMAL"
			if json.Unmarshal(definition.Defaults[name], &p.Value) != nil {
				return nil, cpup01.ErrInvalidArgument
			}
			rule.Type = modeldevv1.ParameterType_PARAMETER_TYPE_DECIMAL
			minimum.Type = "DECIMAL"
			minimum.Value = "0"
			maximum.Type = "DECIMAL"
			maximum.Value = "1"
			if name == "learning_rate" {
				minimumInclusive = false
				rule.NumericRepresentation = "float32_positive_finite"
				rule.ValidationDescription = "non-exponent decimal string, at most 32 characters, positive and finite after float32 conversion"
			} else {
				maximumInclusive = false
				rule.NumericRepresentation = "float64"
				rule.ValidationDescription = "non-exponent decimal string, at most 32 characters, finite float64 in [0,1)"
			}
		case "seed":
			minimum.Value = "0"
			maximum.Value = definition.SeedMax
			rule.NumericRepresentation = "uint32"
			rule.ValidationDescription = "uint32 seed; finetuning data_seed is derived from this same value"
		case "cutoff_len":
			rule.ValidationDescription = "positive int64, not above the resolved model and tokenizer sequence boundary"
		}
		encoded, err := encodeParameters([]cpup01.Parameter{p})
		if err != nil {
			return nil, err
		}
		rule.DefaultValue = encoded[0]
		encoded, err = encodeParameters([]cpup01.Parameter{minimum})
		if err != nil {
			return nil, err
		}
		rule.Minimum = encoded[0]
		rule.MinimumInclusive = &minimumInclusive
		if name != "learning_rate" {
			encoded, err = encodeParameters([]cpup01.Parameter{maximum})
			if err != nil {
				return nil, err
			}
			rule.Maximum = encoded[0]
			rule.MaximumInclusive = &maximumInclusive
		}
		result = append(result, rule)
	}
	return result, nil
}
