package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
)

func TestConfiguredScanRejectsRetiredFieldsWithoutExposingValues(t *testing.T) {
	for _, document := range []string{
		`{"runtime":{"binding_file":"sensitive-legacy-binding"}}`,
		`{"runtime":{"binding_sha256":"sensitive-legacy-digest"}}`,
		`{"command":{"admission_resolution":{"facts_files":[]}}}`,
		`{"command":{"model":{"base_model_storage_scope":{"approved_prefix":"sensitive-legacy-tenant"}}}}`,
		`{"runtime":{"bindingFile":"sensitive-legacy-binding"}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		loader := config.New(config.WithSource(file.NewSource(path)))
		t.Cleanup(func() { _ = loader.Close() })
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		var ignored conf.Bootstrap
		if err := loader.Scan(&ignored); err != nil {
			t.Fatal("test requires the pinned Kratos unknown-field behavior", err)
		}
		value, err := scanBootstrapConfig(loader)
		if value != nil || err == nil || err.Error() != "configuration schema invalid" || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("retired configuration silently survived schema scan", err)
		}
	}
}

func TestConfiguredScanAllowsCurrentEnvironmentExpansionButRejectsRetiredEnvironment(t *testing.T) {
	t.Setenv("ANI_SERVER_GRPC_ADDR", "127.0.0.1:19090")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  grpc:\n    addr: ${SERVER_GRPC_ADDR}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	load := func() (*conf.Bootstrap, error) {
		loader := config.New(config.WithSource(file.NewSource(path), env.NewSource("ANI")))
		defer loader.Close()
		if err := loader.Load(); err != nil {
			return nil, err
		}
		return scanBootstrapConfig(loader)
	}
	value, err := load()
	if err != nil || value.GetServer().GetGrpc().GetAddr() != "127.0.0.1:19090" {
		t.Fatal("normal framework environment expansion failed", err)
	}
	t.Setenv("ANI_RUNTIME_BINDING_FILE", "retired-sensitive-reference")
	value, err = load()
	if err == nil || value != nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("retired environment expansion key accepted", err)
	}
}

func TestConfiguredScanAcceptsCurrentResolvedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"runtime":{"tenant_environment_file":"/platform/config.json","tenant_environment_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"command":{"admission_resolution":{"catalogue_directory":"/materials"},"model":{"base_model_storage_scope":{"connection_id":"model-store","bucket":"models","credential_reference":"read-model"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loader := config.New(config.WithSource(file.NewSource(path)))
	t.Cleanup(func() { _ = loader.Close() })
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	value, err := scanBootstrapConfig(loader)
	if err != nil || value.Runtime.TenantEnvironmentFile != "/platform/config.json" || value.Command.AdmissionResolution.CatalogueDirectory != "/materials" {
		t.Fatal("current resolved schema rejected", err)
	}
}
