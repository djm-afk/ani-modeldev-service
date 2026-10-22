package cpup01_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

const finetuningRequest = `{"name":"CPU FT","kind":"FINETUNING","preset_id":"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA","dataset_version_id":"BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB","base_model_version_id":"CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC","finetuning":{},"new_model_name":"cpu-ft-demo"}`
const finetuningCanonical = `{"schema":"ani.modeldev.finetuning-intent.v1","name":"CPU FT","kind":"FINETUNING","preset_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","dataset_version_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","base_model_version_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","finetuning":{},"new_model_name":"cpu-ft-demo"}`

func TestFinetuningIntentGoldenPresenceAndStaticCodec(t *testing.T) {
	intent, err := cpup01.ParseIntent([]byte(finetuningRequest))
	if err != nil {
		t.Fatal(err)
	}
	raw, absentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != finetuningCanonical {
		t.Fatalf("canonical %s", raw)
	}
	// Independently calculated from the normative literal with Fedora hashlib.
	if absentHash != "13d0a782d203b42f51e50105b579f2609c952c0a8014abfb075e4b4d7fe2ae85" {
		t.Fatalf("finetuning intent digest %s", absentHash)
	}
	explicit := strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":20,"learning_rate":"0.0001000"}`, 1)
	intent, err = cpup01.ParseFinetuningIntent([]byte(explicit))
	if err != nil {
		t.Fatal(err)
	}
	raw, explicitHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if absentHash == explicitHash || !bytes.Contains(raw, []byte(`"finetuning":{"learning_rate":"0.0001","max_steps":20}`)) {
		t.Fatal("presence or canonical exact decimal lost")
	}
	if *intent.Finetuning.LearningRate != "0.0001000" {
		t.Fatal("caller intent mutated")
	}
	// Request structure and lexical identity precede reading a current Release.
	// A well-spelled candidate can be hashed for key lookup, then new admission
	// checks its program hard boundary and the current approved combinations.
	for _, value := range []string{`{"learning_rate":"1","max_steps":200}`, `{"learning_rate":"0","max_steps":0}`, `{"learning_rate":"0.0002","max_steps":10}`} {
		candidate := strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":`+value, 1)
		parsed, err := cpup01.ParseIntent([]byte(candidate))
		if err != nil {
			t.Fatalf("static codec prematurely read rules: %v", err)
		}
		if _, _, err := cpup01.CanonicalIntent(parsed); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinetuningCodecRejectsUnopenedFieldsAndAmbiguousNumbers(t *testing.T) {
	cases := map[string]string{
		"missing object":            strings.Replace(finetuningRequest, `,"finetuning":{}`, "", 1),
		"null object":               strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":null`, 1),
		"duplicate object":          strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{},"finetuning":{}`, 1),
		"case key":                  strings.Replace(finetuningRequest, `"finetuning"`, `"Finetuning"`, 1),
		"duplicate field":           strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":20,"max_steps":10}`, 1),
		"wrong learning type":       strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"learning_rate":0.0001}`, 1),
		"float integer":             strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":20.0}`, 1),
		"string integer":            strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":"20"}`, 1),
		"exponent integer":          strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":2e1}`, 1),
		"overflow integer":          strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":9223372036854775808}`, 1),
		"exponent decimal":          strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"learning_rate":"1e-4"}`, 1),
		"null value":                strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"max_steps":null}`, 1),
		"unopened same fixed value": strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"lora_rank":8}`, 1),
		"managed override":          strings.Replace(finetuningRequest, `"finetuning":{}`, `"finetuning":{"command":"run"}`, 1),
		"general parameters":        strings.TrimSuffix(finetuningRequest, "}") + `,"general_parameters":[]}`,
		"image override":            strings.TrimSuffix(finetuningRequest, "}") + `,"image_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`,
		"tenant override":           strings.TrimSuffix(finetuningRequest, "}") + `,"tenant_id":"forged"}`,
		"source execution":          strings.TrimSuffix(finetuningRequest, "}") + `,"source_execution_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`,
		"missing target":            strings.Replace(finetuningRequest, `,"new_model_name":"cpu-ft-demo"`, "", 1),
		"both targets":              strings.TrimSuffix(finetuningRequest, "}") + `,"target_model_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`,
		"alias target":              strings.Replace(finetuningRequest, `"new_model_name":"cpu-ft-demo"`, `"target_model_id":"model-alias"`, 1),
		"provider name grammar":     strings.Replace(finetuningRequest, "cpu-ft-demo", "CPU Demo", 1),
		"trailing":                  finetuningRequest + `{}`,
		"unpaired surrogate":        strings.Replace(finetuningRequest, "CPU FT", `CPU\ud800`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := cpup01.ParseIntent([]byte(raw)); !errors.Is(err, cpup01.ErrInvalidArgument) {
				t.Fatalf("accepted %s: %v", raw, err)
			}
		})
	}
}

func candidateABProgram() cpup01.ReleaseProgram {
	program := conformance.FinetuningReleaseV1().Program
	b := []cpup01.Parameter{{Name: "learning_rate", Type: "DECIMAL", Value: "0.0002"}, {Name: "max_steps", Type: "INTEGER", Value: "10"}}
	program.AllowedParameterSets = append(program.AllowedParameterSets, b)
	for i := range program.ParameterRules {
		for _, parameter := range b {
			if program.ParameterRules[i].Name == parameter.Name {
				program.ParameterRules[i].AllowedValues = append(program.ParameterRules[i].AllowedValues, parameter)
			}
		}
	}
	return program
}

func TestFinetuningDefaultsAndOnlyApprovedCompleteCombinations(t *testing.T) {
	program := candidateABProgram()
	learningA, learningB := "0.0001", "0.0002"
	stepsA, stepsB := int64(20), int64(10)
	cases := []struct {
		name                string
		explicit            cpup01.FinetuningParameters
		wantRate, wantSteps string
		valid               bool
	}{
		{"empty", cpup01.FinetuningParameters{}, "0.0001", "20", true},
		{"explicit default", cpup01.FinetuningParameters{LearningRate: &learningA, MaxSteps: &stepsA}, "0.0001", "20", true},
		{"partial B rate", cpup01.FinetuningParameters{LearningRate: &learningB}, "", "", false},
		{"partial B steps", cpup01.FinetuningParameters{MaxSteps: &stepsB}, "", "", false},
		{"complete B", cpup01.FinetuningParameters{LearningRate: &learningB, MaxSteps: &stepsB}, "0.0002", "10", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := cpup01.ResolveFinetuningParameters(program, test.explicit)
			if !test.valid {
				if !errors.Is(err, cpup01.ErrInvalidArgument) {
					t.Fatalf("invalid combo accepted %v", resolved)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(resolved) != 2 || resolved[0].Value != test.wantRate || resolved[1].Value != test.wantSteps {
				t.Fatalf("wrong resolved %v", resolved)
			}
		})
	}
	if _, err := cpup01.ResolveFinetuningParameters(conformance.FinetuningReleaseV1().Program, cpup01.FinetuningParameters{LearningRate: &learningB, MaxSteps: &stepsB}); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("unvalidated B enabled by default")
	}
	if program.DefaultParameters[1].Value != "20" {
		t.Fatal("resolver mutated Release defaults")
	}
}

func TestFinetuningReleaseRejectsInvalidRuleDefinitions(t *testing.T) {
	cases := []struct {
		name   string
		change func(*cpup01.ReleaseProgram)
	}{
		{"required rate", func(p *cpup01.ReleaseProgram) { p.ParameterRules[0].Required = true }},
		{"required steps", func(p *cpup01.ReleaseProgram) { p.ParameterRules[1].Required = true }},
		{"empty rules", func(p *cpup01.ReleaseProgram) { p.ParameterRules = nil }},
		{"duplicate names", func(p *cpup01.ReleaseProgram) { p.ParameterRules[1] = p.ParameterRules[0] }},
		{"fixed setting rule", func(p *cpup01.ReleaseProgram) { p.ParameterRules[0].Name = "lora_rank" }},
		{"wrong type", func(p *cpup01.ReleaseProgram) { p.ParameterRules[1].Type = "DECIMAL" }},
		{"empty allowed", func(p *cpup01.ReleaseProgram) { p.ParameterRules[0].AllowedValues = nil }},
		{"empty combinations", func(p *cpup01.ReleaseProgram) { p.AllowedParameterSets = nil }},
		{"duplicate combinations", func(p *cpup01.ReleaseProgram) {
			p.AllowedParameterSets = append(p.AllowedParameterSets, p.AllowedParameterSets[0])
		}},
		{"default not approved", func(p *cpup01.ReleaseProgram) { p.DefaultParameters[0].Value = "0.0002" }},
		{"program max outside", func(p *cpup01.ReleaseProgram) { p.DefaultParameters[1].Value = "201" }},
		{"program rate outside", func(p *cpup01.ReleaseProgram) { p.DefaultParameters[0].Value = "1.01" }},
		{"duplicate defaults", func(p *cpup01.ReleaseProgram) { p.DefaultParameters[1] = p.DefaultParameters[0] }},
		{"mixed enumeration range", func(p *cpup01.ReleaseProgram) {
			minimum := p.DefaultParameters[0]
			p.ParameterRules[0].Minimum = &minimum
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			program := conformance.FinetuningReleaseV1().Program
			test.change(&program)
			if err := cpup01.ValidateFinetuningProgramParameters(program); !errors.Is(err, cpup01.ErrInvalidArgument) {
				t.Fatalf("invalid rule accepted: %v", err)
			}
		})
	}
	program := candidateABProgram()
	open, closed := false, true
	minimum, maximum := cpup01.Parameter{Name: "learning_rate", Type: "DECIMAL", Value: "0"}, cpup01.Parameter{Name: "learning_rate", Type: "DECIMAL", Value: "1"}
	program.ParameterRules[0] = cpup01.ParameterRule{Name: "learning_rate", Type: "DECIMAL", Minimum: &minimum, Maximum: &maximum, MinimumInclusive: &open, MaximumInclusive: &closed}
	if err := cpup01.ValidateFinetuningProgramParameters(program); err != nil {
		t.Fatalf("valid exact-decimal range rejected: %v", err)
	}
	program.ParameterRules[0].MinimumInclusive = nil
	if err := cpup01.ValidateFinetuningProgramParameters(program); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("range with implicit inclusiveness accepted")
	}
}

func TestFinetuningImmutableSnapshotAndEnvelopeCoherence(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	raw, err := snapshot.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var replay cpup01.Snapshot
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	again, err := replay.Canonical()
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("snapshot replay changed: %v", err)
	}
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	envelope := cpup01.AdmissionEnvelope{TenantID: "11111111-1111-4111-8111-111111111111", OperationID: "22222222-2222-4222-8222-222222222222", ExecutionID: "33333333-3333-4333-8333-333333333333", Actor: "governance:user:contract", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour)}
	if _, _, err := envelope.CanonicalPayloads(); err != nil {
		t.Fatal(err)
	}
	changed := "44444444-4444-4444-8444-444444444444"
	envelope.Intent.BaseModelVersionID = &changed
	_, envelope.IntentHash, _ = cpup01.CanonicalIntent(envelope.Intent)
	if _, _, err := envelope.CanonicalPayloads(); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("base source mismatch accepted")
	}
	snapshot.Finetuning.ManagedSettings.LoraRank = 16
	if err := snapshot.Validate(); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("managed override accepted")
	}
}

func TestFinetuningReleaseCanonicalRejectsWrongComponentArguments(t *testing.T) {
	release := conformance.FinetuningReleaseV1()
	raw, err := release.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := cpup01.ParseRelease(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parsed.Canonical()
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("immutable release changed")
	}
	release.Finetuning.ExportProgram.ArgsTemplate[5].Literal = "--fake-input"
	if _, err := release.Canonical(); !errors.Is(err, cpup01.ErrInvalidArgument) {
		t.Fatal("broken managed argv accepted")
	}
}

func TestFinetuningConfigurationGoldenMatchesManagedEntryProjection(t *testing.T) {
	configuration := cpup01.FinetuningConfiguration{
		ParameterContractVersion: cpup01.FinetuningParameterContractVersion,
		Parameters:               conformance.FinetuningReleaseV1().Program.DefaultParameters,
		ManagedSettings:          conformance.FinetuningManagedSettingsV1(),
	}
	raw, err := configuration.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"parameter_contract_version":"ani.finetune.lora.parameters.v1","parameters":[{"name":"learning_rate","type":"DECIMAL","value":"0.0001"},{"name":"max_steps","type":"INTEGER","value":"20"}],"managed_settings":{"stage":"sft","finetuning_type":"lora","lora_target":"all","lora_rank":8,"lora_alpha":16,"cutoff_len":128,"per_device_train_batch_size":1,"gradient_accumulation_steps":1,"seed":42,"data_seed":42,"device":"cpu","fp16":false,"bf16":false,"flash_attn":"disabled","report_to":"none","save_strategy":"no","template":"qwen","train_dtype":"float32","export_dtype":"float32","verify_dtype":"float32","lr_scheduler_type":"linear","warmup_ratio":"0","weight_decay":"0","lora_dropout":"0","num_train_epochs":3,"optim":"adamw_torch","logging_steps":1}}`
	if string(raw) != want {
		t.Fatalf("configuration projection %s", raw)
	}
	digest, err := configuration.Digest()
	if err != nil || digest != "86d88f906754f7d71c366208433c5f500cc10e566ae6969b6429f07ffed208a8" {
		t.Fatalf("configuration digest %s: %v", digest, err)
	}
	configuration.Parameters[0].Value = "0.0002"
	changed, err := configuration.Canonical()
	if err != nil || bytes.Equal(raw, changed) {
		t.Fatal("nondefault configuration was filtered")
	}
}
