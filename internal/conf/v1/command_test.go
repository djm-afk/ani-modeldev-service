package conf

import "testing"

func TestConfiguredCommandRequiresExplicitConnectionReferences(t *testing.T) {
	tests := []struct {
		name   string
		change func(*GovernanceCommand)
	}{
		{"missing database", func(c *GovernanceCommand) { c.DatabaseUrlFile = "" }},
		{"raw database URL", func(c *GovernanceCommand) { c.DatabaseUrlFile = "postgres://test.invalid/database" }},
		{"relative database file", func(c *GovernanceCommand) { c.DatabaseUrlFile = "private/database" }},
		{"missing CA", func(c *GovernanceCommand) { c.ClientCaFile = "" }},
		{"unknown CA", func(c *GovernanceCommand) { c.ClientCaFile = "UNKNOWN" }},
		{"missing certificate", func(c *GovernanceCommand) { c.CertificateFile = "" }},
		{"missing key", func(c *GovernanceCommand) { c.PrivateKeyFile = "" }},
		{"missing identity", func(c *GovernanceCommand) { c.GovernanceDnsName = "" }},
		{"placeholder identity", func(c *GovernanceCommand) { c.GovernanceDnsName = "NOT_READY" }},
		{"wildcard identity", func(c *GovernanceCommand) { c.GovernanceDnsName = "*.example.test" }},
		{"identity URL", func(c *GovernanceCommand) { c.GovernanceDnsName = "https://ani-governance" }},
		{"identity whitespace", func(c *GovernanceCommand) { c.GovernanceDnsName = " ani-governance" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			tt.change(config.Command)
			if err := config.Validate(); err == nil {
				t.Fatal("configured command accepted missing or untrusted deployment inputs")
			}
		})
	}
	config := validConfig()
	config.Command = commandConfiguration()
	if err := config.Validate(); err != nil {
		t.Fatalf("complete command references rejected: %v", err)
	}
}

func commandConfiguration() *GovernanceCommand {
	return &GovernanceCommand{DatabaseUrlFile: "/run/secrets/modeldev/database", ClientCaFile: "/run/secrets/modeldev/ca.crt", CertificateFile: "/run/secrets/modeldev/tls.crt", PrivateKeyFile: "/run/secrets/modeldev/tls.key", GovernanceDnsName: "ani-governance"}
}
