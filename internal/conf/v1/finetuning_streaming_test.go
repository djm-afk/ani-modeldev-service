package conf

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestFinetuningStreamingConfigurationBudgets(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Bootstrap)
		valid  bool
	}{
		{name: "existing defaults", valid: true},
		{name: "managed fine budgets", valid: true, change: func(c *Bootstrap) {
			c.Runtime.Step.Timeout = durationpb.New(3600 * time.Second)
			c.Runtime.ObjectStorage.StreamingTimeout = durationpb.New(3600 * time.Second)
		}},
		{name: "step above finite maximum", change: func(c *Bootstrap) { c.Runtime.Step.Timeout = durationpb.New(24*time.Hour + time.Second) }},
		{name: "command stays bounded by 30", change: func(c *Bootstrap) { c.Server.Grpc.Timeout = durationpb.New(31 * time.Second) }},
		{name: "admin stays bounded by 30", change: func(c *Bootstrap) { c.Server.Admin.Timeout = durationpb.New(31 * time.Second) }},
		{name: "API stays bounded by 30", change: func(c *Bootstrap) { c.Runtime.ApiTimeout = durationpb.New(31 * time.Second) }},
		{name: "explicit zero streaming", change: func(c *Bootstrap) { c.Runtime.ObjectStorage.StreamingTimeout = durationpb.New(0) }},
		{name: "streaming above finite maximum", change: func(c *Bootstrap) {
			c.Runtime.ObjectStorage.StreamingTimeout = durationpb.New(24*time.Hour + time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := validConfig()
			c.Command = commandConfiguration()
			c.Runtime = &ManagedRuntime{
				Step:            &Server_GRPC{Network: "tcp", Addr: "127.0.0.1:19092", Timeout: durationpb.New(30 * time.Second)},
				CertificateFile: c.Command.CertificateFile, PrivateKeyFile: c.Command.PrivateKeyFile,
				Kubernetes:    &HTTPSWorkloadConnection{Endpoint: "https://kube.example.test", CaFile: "/owner/kube-ca", TokenFile: "/owner/kube-token"},
				Pipeline:      &HTTPSWorkloadConnection{Endpoint: "https://pipeline.example.test", CaFile: "/owner/pipeline-ca", TokenFile: "/owner/pipeline-token"},
				ObjectStorage: &ObjectStorageConnection{Endpoint: "https://storage.example.test", CaFile: "/owner/storage-ca", CredentialsFile: "/owner/storage-credentials", Region: "us-east-1", ConnectionId: "artifact-store-v1", MaxObjectBytes: 64 << 20},
				BindingFile:   "/owner/binding.json", BindingSha256: strings.Repeat("a", 64), TokenAudience: "ani-managed-step",
				ApiTimeout: durationpb.New(30 * time.Second), DispatchInterval: durationpb.New(time.Second), DispatchBatchSize: 4,
			}
			if test.change != nil {
				test.change(c)
			}
			if err := c.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid=%t", err, test.valid)
			}
		})
	}
}
