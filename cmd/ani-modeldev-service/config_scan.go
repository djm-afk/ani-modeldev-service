package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Scan the resolved Kratos document through the current protobuf schema.
// Kratos' protobuf Scan discards unknown fields, which would hide retired
// tenant binding, facts and STS configuration after this incompatible cutover.
func scanBootstrapConfig(source interface{ Scan(any) error }) (*conf.Bootstrap, error) {
	var raw json.RawMessage
	if source.Scan(&raw) != nil {
		return nil, errors.New("configuration schema invalid")
	}
	// Kratos keeps the environment source's flat expansion keys alongside the
	// resolved file tree. Strip only current-schema keys supplied by that exact
	// ANI_ environment variable; unknown and retired file keys remain errors.
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		return nil, errors.New("configuration schema invalid")
	}
	keys := make(map[string]bool)
	bootstrapEnvironmentKeys((&conf.Bootstrap{}).ProtoReflect().Descriptor(), "", keys)
	for key, value := range document {
		expected, exists := os.LookupEnv("ANI_" + key)
		var actual string
		if keys[key] && exists && json.Unmarshal(value, &actual) == nil && actual == expected {
			delete(document, key)
		}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("configuration schema invalid")
	}
	var result conf.Bootstrap
	if protojson.Unmarshal(raw, &result) != nil {
		return nil, errors.New("configuration schema invalid")
	}
	return &result, nil
}

func bootstrapEnvironmentKeys(message protoreflect.MessageDescriptor, prefix string, keys map[string]bool) {
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		key := prefix + strings.ToUpper(string(field.Name()))
		keys[key] = true
		if field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap() && !strings.HasPrefix(string(field.Message().FullName()), "google.protobuf.") {
			bootstrapEnvironmentKeys(field.Message(), key+"_", keys)
		}
	}
}
