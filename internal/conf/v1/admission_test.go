package conf

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOptionalAdmissionResolutionValidatesShapeWithoutReadingMaterials(t *testing.T) {
	for _, count := range []int{0, 1, 64} {
		t.Run(strconv.Itoa(count)+" catalogue references", func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			if count != 0 {
				config.Command.AdmissionResolution = admissionConfiguration(t, count)
			}
			// These references point beneath a new empty directory. Validate
			// checks only shape; actual material loading belongs to startup.
			if err := config.Validate(); err != nil {
				t.Fatalf("optional or structurally valid admission references rejected: %v", err)
			}
		})
	}
}

func TestConfiguredAdmissionResolutionRejectsIncompleteMaterialShape(t *testing.T) {
	tests := []struct {
		name   string
		change func(*AdmissionResolution)
	}{
		{"explicit empty block", func(a *AdmissionResolution) { *a = AdmissionResolution{} }},
		{"missing catalogue", func(a *AdmissionResolution) { a.CatalogueDirectory = "" }},
		{"relative catalogue", func(a *AdmissionResolution) { a.CatalogueDirectory = "sensitive-catalogue/reference" }},
		{"catalogue URL", func(a *AdmissionResolution) { a.CatalogueDirectory = "https://sensitive-material.invalid/catalogue" }},
		{"unclean catalogue", func(a *AdmissionResolution) { a.CatalogueDirectory += "/../sensitive-catalogue" }},
		{"catalogue NUL", func(a *AdmissionResolution) { a.CatalogueDirectory += "\x00sensitive-catalogue" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			config.Command.AdmissionResolution = admissionConfiguration(t, 2)
			tt.change(config.Command.AdmissionResolution)
			err := config.Validate()
			if err == nil {
				t.Fatal("configured admission accepted an incomplete or untrusted material shape")
			}
			message := err.Error()
			if len(message) > 256 || strings.ContainsAny(message, "\x00\r\n") {
				t.Error("admission configuration error must be bounded single-line text")
			}
			references := []string{config.Command.AdmissionResolution.CatalogueDirectory,
				config.Command.DatabaseUrlFile, config.Command.ClientCaFile, config.Command.CertificateFile, config.Command.PrivateKeyFile, config.Command.GovernanceDnsName}
			for _, reference := range references {
				if reference != "" && strings.Contains(message, reference) {
					t.Error("admission configuration error exposed a configured value")
				}
			}
		})
	}
}

func TestValidAdmissionResolutionDoesNotSkipRemainingServerValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Bootstrap)
	}{
		{"grpc listener", func(c *Bootstrap) { c.Server.Grpc.Network = "udp" }},
		{"admin listener", func(c *Bootstrap) { c.Server.Admin.Network = "unix" }},
		{"distinct listener ports", func(c *Bootstrap) { c.Server.Admin.Addr = c.Server.Grpc.Addr }},
		{"grpc timeout", func(c *Bootstrap) { c.Server.Grpc.Timeout = nil }},
		{"admin timeout", func(c *Bootstrap) { c.Server.Admin.Timeout = nil }},
		{"shutdown timeout", func(c *Bootstrap) { c.Server.ShutdownTimeout = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			config.Command.AdmissionResolution = admissionConfiguration(t, 1)
			tt.change(config)
			if err := config.Validate(); err == nil {
				t.Fatal("valid admission references bypassed server validation")
			}
		})
	}
}

func admissionConfiguration(t *testing.T, count int) *AdmissionResolution {
	t.Helper()
	root := t.TempDir()
	return &AdmissionResolution{CatalogueDirectory: filepath.Join(root, "sensitive-catalogue")}
}
