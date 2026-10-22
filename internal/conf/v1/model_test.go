package conf

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestConfiguredModelConnectionRequiresExplicitTrustedMaterials(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ModelConnection)
	}{
		{"missing endpoint", func(c *ModelConnection) { c.Endpoint = "" }},
		{"endpoint URL", func(c *ModelConnection) { c.Endpoint = "https://ani-model.ani.internal:19090" }},
		{"endpoint without port", func(c *ModelConnection) { c.Endpoint = "ani-model.ani.internal" }},
		{"endpoint with path", func(c *ModelConnection) { c.Endpoint = "ani-model.ani.internal:19090/path" }},
		{"endpoint zero port", func(c *ModelConnection) { c.Endpoint = "ani-model.ani.internal:0" }},
		{"endpoint whitespace", func(c *ModelConnection) { c.Endpoint = " ani-model.ani.internal:19090" }},
		{"relative CA", func(c *ModelConnection) { c.CaFile = "ca.crt" }},
		{"missing CA", func(c *ModelConnection) { c.CaFile = "" }},
		{"missing client certificate", func(c *ModelConnection) { c.CertificateFile = "" }},
		{"missing client key", func(c *ModelConnection) { c.PrivateKeyFile = "" }},
		{"missing client identity", func(c *ModelConnection) { c.ClientDnsName = "" }},
		{"wildcard client identity", func(c *ModelConnection) { c.ClientDnsName = "*.ani.internal" }},
		{"client identity URL", func(c *ModelConnection) { c.ClientDnsName = "https://ani-modeldev" }},
		{"client identity placeholder", func(c *ModelConnection) { c.ClientDnsName = "unknown" }},
		{"missing server identity", func(c *ModelConnection) { c.ServerName = "" }},
		{"wildcard server identity", func(c *ModelConnection) { c.ServerName = "*.ani.internal" }},
		{"identical client and server identity", func(c *ModelConnection) { c.ServerName = c.ClientDnsName }},
		{"missing actor", func(c *ModelConnection) { c.Actor = "" }},
		{"actor with newline", func(c *ModelConnection) { c.Actor = "modeldev:service\n" }},
		{"missing timeout", func(c *ModelConnection) { c.Timeout = nil }},
		{"zero timeout", func(c *ModelConnection) { c.Timeout = durationpb.New(0) }},
		{"timeout above maximum", func(c *ModelConnection) { c.Timeout = durationpb.New(31 * time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			config.Command.Model = modelConnectionConfiguration()
			tt.change(config.Command.Model)
			if err := config.Validate(); err == nil {
				t.Fatal("configured Model connection accepted missing or untrusted deployment inputs")
			}
		})
	}
	config := validConfig()
	config.Command = commandConfiguration()
	config.Command.Model = modelConnectionConfiguration()
	if err := config.Validate(); err != nil {
		t.Fatalf("complete Model connection references rejected: %v", err)
	}
}

// An absent Model block must keep the command composition valid: FINETUNING
// stays disabled instead of failing the CPU command listener at startup.
func TestAbsentModelConnectionKeepsCommandValid(t *testing.T) {
	config := validConfig()
	config.Command = commandConfiguration()
	if config.Command.Model != nil {
		t.Fatal("test precondition: Model connection must be absent")
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("command without a Model connection was rejected: %v", err)
	}
}

func modelConnectionConfiguration() *ModelConnection {
	return &ModelConnection{
		Endpoint:        "ani-model-grpc.ani-system.svc.cluster.local:19090",
		CaFile:          "/run/secrets/modeldev/model/ca.crt",
		ClientDnsName:   "ani-modeldev.ani.internal",
		CertificateFile: "/run/secrets/modeldev/model/tls.crt",
		PrivateKeyFile:  "/run/secrets/modeldev/model/tls.key",
		ServerName:      "ani-model.ani.internal",
		Actor:           "modeldev:service",
		Timeout:         durationpb.New(30 * time.Second),
		ArtifactPlan: &ModelArtifactPlan{
			FileName:     "model.tar",
			Format:       "safetensors",
			DisplayName:  "CPU finetuned model",
			Description:  "CPU finetuning output",
			Capabilities: []string{"text-generation"},
		},
	}
}

// A Model connection that names no artifact would record every registration as
// unavailable, so an incomplete plan is rejected at startup rather than
// silently disabling the feature. The validator is exercised directly because
// the full-config path additionally requires platform-specific absolute mount
// references that only the Linux gate can satisfy.
func TestConfiguredModelConnectionRequiresArtifactPlan(t *testing.T) {
	tests := []struct {
		name   string
		plan   *ModelArtifactPlan
		change func(*ModelArtifactPlan)
	}{
		{name: "missing plan", plan: nil},
		{name: "missing file name", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.FileName = "" }},
		{name: "file name with separator", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.FileName = "out/model.tar" }},
		{name: "missing format", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.Format = "" }},
		{name: "format with whitespace", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.Format = "safe tensors" }},
		{name: "display name too long", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.DisplayName = strings.Repeat("a", 129) }},
		{name: "description too long", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.Description = strings.Repeat("a", 513) }},
		{name: "too many capabilities", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) {
			p.Capabilities = make([]string, 17)
			for i := range p.Capabilities {
				p.Capabilities[i] = "chat"
			}
		}},
		{name: "empty capability", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.Capabilities = []string{""} }},
		{name: "capability with space", plan: validArtifactPlanConfiguration(), change: func(p *ModelArtifactPlan) { p.Capabilities = []string{"text generation"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.change != nil {
				tt.change(tt.plan)
			}
			if err := validateModelArtifactPlan(tt.plan); err == nil {
				t.Fatal("Model artifact plan accepted unusable values")
			}
		})
	}
	// Metadata is optional: the frozen intent, not this plan, decides whether a
	// registration creates a container.
	if err := validateModelArtifactPlan(&ModelArtifactPlan{FileName: "model.tar", Format: "safetensors"}); err != nil {
		t.Fatalf("minimal artifact plan rejected: %v", err)
	}
}

func validArtifactPlanConfiguration() *ModelArtifactPlan {
	return &ModelArtifactPlan{
		FileName:     "model.tar",
		Format:       "safetensors",
		DisplayName:  "CPU finetuned model",
		Description:  "CPU finetuning output",
		Capabilities: []string{"text-generation"},
	}
}

// A configured controlled substitute must carry explicit absolute references
// and lowercase SHA256 pins; an incomplete block is rejected rather than
// silently disabling FINETUNING after an operator intended to enable it.
func TestControlledModelFactsRequirePinnedMaterials(t *testing.T) {
	tests := []struct {
		name string
		file *AdmissionFactsFile
		ok   bool
	}{
		{name: "valid pin", file: &AdmissionFactsFile{Path: "/run/secrets/modeldev/model/base.json", Sha256: strings.Repeat("a", 64)}, ok: true},
		{name: "relative path", file: &AdmissionFactsFile{Path: "base.json", Sha256: strings.Repeat("a", 64)}},
		{name: "missing path", file: &AdmissionFactsFile{Sha256: strings.Repeat("a", 64)}},
		{name: "short digest", file: &AdmissionFactsFile{Path: "/run/secrets/modeldev/model/base.json", Sha256: strings.Repeat("a", 63)}},
		{name: "uppercase digest", file: &AdmissionFactsFile{Path: "/run/secrets/modeldev/model/base.json", Sha256: strings.Repeat("A", 64)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			config.Command.ControlledModelFacts = &ControlledModelFacts{Files: []*AdmissionFactsFile{tt.file}}
			err := config.Validate()
			if tt.ok && err != nil {
				t.Fatalf("valid controlled facts rejected: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("invalid controlled facts accepted")
			}
		})
	}
	config := validConfig()
	config.Command = commandConfiguration()
	config.Command.ControlledModelFacts = &ControlledModelFacts{}
	if err := config.Validate(); err == nil {
		t.Fatal("empty controlled facts block accepted")
	}
}

// An absent controlled substitute keeps the command composition valid so the
// CPU command listener still starts with FINETUNING disabled.
func TestAbsentControlledModelFactsKeepsCommandValid(t *testing.T) {
	config := validConfig()
	config.Command = commandConfiguration()
	if config.Command.ControlledModelFacts != nil {
		t.Fatal("test precondition: controlled facts must be absent")
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("command without controlled facts was rejected: %v", err)
	}
}
