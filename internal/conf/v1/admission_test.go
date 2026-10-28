package conf

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestOptionalAdmissionResolutionValidatesShapeWithoutReadingMaterials(t *testing.T) {
	for _, count := range []int{0, 1, 64} {
		t.Run(strconv.Itoa(count)+" facts files", func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			if count != 0 {
				config.Command.AdmissionResolution = admissionConfiguration(t, count)
				config.ImageRuntime = imageRuntimeConfiguration(t)
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
		{"no facts files", func(a *AdmissionResolution) { a.FactsFiles = nil }},
		{"too many facts files", func(a *AdmissionResolution) {
			original := a.FactsFiles[0]
			a.FactsFiles = make([]*AdmissionFactsFile, 65)
			for i := range a.FactsFiles {
				a.FactsFiles[i] = &AdmissionFactsFile{Path: original.Path + strconv.Itoa(i), Sha256: original.Sha256}
			}
		}},
		{"nil later facts file", func(a *AdmissionResolution) { a.FactsFiles[1] = nil }},
		{"missing later facts path", func(a *AdmissionResolution) { a.FactsFiles[1].Path = "" }},
		{"relative later facts path", func(a *AdmissionResolution) { a.FactsFiles[1].Path = "sensitive-facts/reference.json" }},
		{"unclean later facts path", func(a *AdmissionResolution) { a.FactsFiles[1].Path += "/../sensitive-facts.json" }},
		{"later facts path NUL", func(a *AdmissionResolution) { a.FactsFiles[1].Path += "\x00sensitive-facts" }},
		{"missing later facts digest", func(a *AdmissionResolution) { a.FactsFiles[1].Sha256 = "" }},
		{"short later facts digest", func(a *AdmissionResolution) { a.FactsFiles[1].Sha256 = strings.Repeat("a", 63) }},
		{"long later facts digest", func(a *AdmissionResolution) { a.FactsFiles[1].Sha256 = strings.Repeat("a", 65) }},
		{"uppercase later facts digest", func(a *AdmissionResolution) { a.FactsFiles[1].Sha256 = strings.Repeat("A", 64) }},
		{"nonhex later facts digest", func(a *AdmissionResolution) { a.FactsFiles[1].Sha256 = strings.Repeat("g", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Command = commandConfiguration()
			config.Command.AdmissionResolution = admissionConfiguration(t, 2)
			config.ImageRuntime = imageRuntimeConfiguration(t)
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
			for _, file := range config.Command.AdmissionResolution.FactsFiles {
				if file != nil {
					references = append(references, file.Path, file.Sha256)
				}
			}
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
			config.ImageRuntime = imageRuntimeConfiguration(t)
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
	admission := &AdmissionResolution{CatalogueDirectory: filepath.Join(root, "sensitive-catalogue"), FactsFiles: make([]*AdmissionFactsFile, count)}
	for i := range admission.FactsFiles {
		admission.FactsFiles[i] = &AdmissionFactsFile{Path: filepath.Join(root, "sensitive-facts-"+strconv.Itoa(i)+".json"), Sha256: strings.Repeat("a", 64)}
	}
	return admission
}

// imageRuntimeConfiguration is the paired Resource dependency admission
// resolution requires. Validate checks shape only, so the files need not exist.
func imageRuntimeConfiguration(t *testing.T) *ImageRuntimeConnection {
	t.Helper()
	root := t.TempDir()
	return &ImageRuntimeConnection{
		Endpoint:        "127.0.0.1:19092",
		CaFile:          filepath.Join(root, "ca.pem"),
		CertificateFile: filepath.Join(root, "client.pem"),
		PrivateKeyFile:  filepath.Join(root, "client.key"),
		ServerName:      "ani-resource-image-runtime",
		Timeout:         durationpb.New(5 * time.Second),
	}
}
