package protobuf

import (
	"encoding/json"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func EncodeRuntimeCapabilities(value cpup01.RuntimeCapabilities) (*modeldevv1.RuntimeCapabilities, error) {
	if _, err := value.Canonical(); err != nil {
		return nil, err
	}
	message := new(modeldevv1.RuntimeCapabilities)
	if err := encodeCurrent(value, message); err != nil {
		return nil, err
	}
	return message, nil
}

func EncodeTemplatePlan(value cpup01.TemplatePlan) (*modeldevv1.TemplatePlan, error) {
	if _, err := value.Canonical(); err != nil {
		return nil, err
	}
	message := new(modeldevv1.TemplatePlan)
	if err := encodeCurrent(value, message); err != nil {
		return nil, err
	}
	return message, nil
}

// Current contract structs and generated messages deliberately share exact
// JSON field names and decimal-string int64 semantics. Use the standard
// protobuf codec for this field-for-field projection; neither side admits
// unknown fields or an alternate contract shape.
func encodeCurrent(value any, message proto.Message) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message)
}

func decodeCurrent(message proto.Message, value any) error {
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
