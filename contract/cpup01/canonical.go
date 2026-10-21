package cpup01

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// encodeCanonicalJSON writes lexical object keys and UTF-8 JSON without HTML
// escapes or a trailing newline. Callers validate their finite contract first.
func encodeCanonicalJSON(value any) ([]byte, error) {
	// Decode with UseNumber so all nested objects use lexical key order without
	// rounding int64 values through float64. This is shared with Python vectors.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// canonicalParameters returns a sorted copy; an explicit empty selection stays
// a JSON array. The caller retains responsibility for optional-field presence.
func canonicalParameters(values []Parameter) []Parameter {
	parameters := append([]Parameter{}, values...)
	for i := range parameters {
		if parameters[i].Type == "DECIMAL" && strings.Contains(parameters[i].Value, ".") {
			parameters[i].Value = strings.TrimRight(strings.TrimRight(parameters[i].Value, "0"), ".")
		}
	}
	sort.Slice(parameters, func(i, j int) bool { return parameters[i].Name < parameters[j].Name })
	return parameters
}
