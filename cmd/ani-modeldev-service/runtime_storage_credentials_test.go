package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMountedStorageOwnerRotatesWithoutAmbientCredentials(t *testing.T) {
	file := filepath.Join(t.TempDir(), "restricted-storage-owner.json")
	write := func(key string) {
		t.Helper()
		data := []byte(`{"access_key_id":"` + key + `","secret_access_key":"synthetic-restricted-owner"}`)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("synthetic-first-owner")
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-owner-must-not-be-used")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret-must-not-be-used")
	provider := mountedStorageCredentials(file)
	first, err := provider.Retrieve(context.Background())
	if err != nil || first.AccessKeyID != "synthetic-first-owner" || first.SessionToken != "" || first.CanExpire {
		t.Fatalf("use only the mounted restricted issuer material: %v", err)
	}
	write("synthetic-rotated-owner")
	renewed, err := provider.Retrieve(context.Background())
	if err != nil || renewed.AccessKeyID != "synthetic-rotated-owner" {
		t.Fatalf("owner rotation must be consumed without restarting the process: %v", err)
	}
}

func TestMountedStorageOwnerRejectsTemporaryCredentialsAndUnknownMaterial(t *testing.T) {
	for _, value := range []string{
		`{"access_key_id":"temporary-key","secret_access_key":"temporary-secret","session_token":"temporary-token","sts_bindings":[]}`,
		`{"access_key_id":"owner-key","secret_access_key":"owner-secret","unexpected":"hidden-endpoint"}`,
	} {
		file := filepath.Join(t.TempDir(), "invalid-owner.json")
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := mountedStorageCredentials(file).Retrieve(context.Background()); err == nil {
			t.Fatal("STS cannot be rooted in temporary or unrecognized credentials")
		}
	}
}
