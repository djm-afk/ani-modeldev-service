package protobuf_test

import (
	"bytes"
	"errors"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestFinetuningTypedProtoRoundTripsPreserveCanonicalPayloads(t *testing.T) {
	intent := conformance.FinetuningIntentV1()
	original, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	message, err := codec.EncodeIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING || message.Finetuning == nil || message.Finetuning.MaxSteps != nil || message.Finetuning.LearningRate != nil {
		t.Fatal("empty finetuning presence lost")
	}
	decoded, err := codec.DecodeIntent(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, decodedHash, err := cpup01.CanonicalIntent(decoded)
	if err != nil || hash != decodedHash || !bytes.Equal(original, raw) {
		t.Fatal("intent proto changed original identity")
	}
	snapshot := conformance.FinetuningSnapshotV1()
	// Application composition can leave empty component selections nil; the
	// canonical contract must still match protobuf's empty repeated fields.
	snapshot.Finetuning.ExportProgram.ResolvedParameters = nil
	snapshot.Finetuning.VerifyProgram.ResolvedParameters = nil
	original, err = snapshot.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	snapshotMessage, err := codec.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotMessage.Finetuning == nil || snapshotMessage.OutputContract.OutputKind != trainingv1.OutputKind_OUTPUT_KIND_MERGED_HF_MODEL || snapshotMessage.OutputContract.MaxTarBytes == nil {
		t.Fatal("finetuning snapshot facts missing")
	}
	decodedSnapshot, err := codec.DecodeSnapshot(snapshotMessage)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = decodedSnapshot.Canonical()
	if err != nil || !bytes.Equal(original, raw) {
		t.Fatalf("snapshot proto changed bytes: %v", err)
	}
	if trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING != 1 || trainingv1.OutputKind_OUTPUT_KIND_CHECKPOINT != 1 || trainingv1.FileRole_FILE_ROLE_LOG != 7 || modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE != 5 {
		t.Fatal("published enum numbers changed")
	}
}

func TestFinetuningTypedProtoRejectsUnknownAndIncompleteFacts(t *testing.T) {
	intent, err := codec.EncodeIntent(conformance.FinetuningIntentV1())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.UserIntent)
	}{
		{"missing object", func(m *modeldevv1.UserIntent) { m.Finetuning = nil }},
		{"nested unknown", func(m *modeldevv1.UserIntent) { m.Finetuning.ProtoReflect().SetUnknown([]byte{0x18, 1}) }},
		{"mixed parameters", func(m *modeldevv1.UserIntent) { m.GeneralParameters = &modeldevv1.ParameterSelection{} }},
		{"unknown kind", func(m *modeldevv1.UserIntent) { m.Kind = trainingv1.ExecutionKind(99) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := proto.Clone(intent).(*modeldevv1.UserIntent)
			test.change(value)
			if _, err := codec.DecodeIntent(value); !errors.Is(err, cpup01.ErrInvalidArgument) {
				t.Fatal("invalid intent accepted")
			}
		})
	}
	snapshot, err := codec.EncodeSnapshot(conformance.FinetuningSnapshotV1())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.ExecutionSnapshot)
	}{
		{"missing base", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.BaseModel = nil }},
		{"missing scope", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.BaseModel.Scope = nil }},
		{"missing budgets", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.Budgets = nil }},
		{"missing settings", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.ManagedSettings = nil }},
		{"missing export", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.ExportProgram = nil }},
		{"unknown settings", func(m *modeldevv1.ExecutionSnapshot) {
			m.Finetuning.ManagedSettings.ProtoReflect().SetUnknown([]byte{0xf0, 1, 1})
		}},
		{"changed args", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.VerifyProgram.ResolvedArgs[0] = "train" }},
		{"null file", func(m *modeldevv1.ExecutionSnapshot) { m.Finetuning.BaseModel.Files[0] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := proto.Clone(snapshot).(*modeldevv1.ExecutionSnapshot)
			test.change(value)
			if _, err := codec.DecodeSnapshot(value); !errors.Is(err, cpup01.ErrInvalidArgument) {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestFinetuningSafeRuleProjectionUsesRealIntegerValue(t *testing.T) {
	program := conformance.FinetuningReleaseV1().Program
	views, err := codec.EncodeFinetuningParameterRules(program)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Name == "max_steps" && view.DefaultValue.GetIntegerValue() != 20 {
			t.Fatalf("max_steps got hard-coded other recipe value %v", view.DefaultValue)
		}
		if view.Required || view.Editable || view.NonEditableReason == "" {
			t.Fatal("single approved value projection wrong")
		}
	}
	program.DefaultParameters[1].Value = "9223372036854775808"
	if _, err := codec.EncodeFinetuningParameterRules(program); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("invalid internal integer silently projected")
	}
}
