package lifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
)

func TestRuntimePublicationPersistsEmptyAncillaryFileAcrossReconnect(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	repository := lifecycle.New(open())
	finishFinetuningArtifactStepsFixture(t, open(), repository, authority, binding)
	publication := publicationFixture(t, admission, authority)
	file := publication.Files[0]
	file.ArtifactID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("empty-model-notice")).String()
	file.File = cpup01.OutputFile{RelativePath: "export/merged/NOTICE.txt", Role: "HF_MODEL", InTar: true, SHA256: cpup01.EmptyContentSHA256}
	file.Object.Key = strings.TrimSuffix(admission.Snapshot.PublicationScope.ApprovedPrefix, "/") + "/" + admission.ExecutionID + "/" + file.File.RelativePath
	file.Object.SizeBytes, file.Object.SHA256 = 0, cpup01.EmptyContentSHA256
	publication.Files = append(publication.Files, file)
	files := make([]cpup01.OutputFile, 0, len(publication.Files))
	for _, entry := range publication.Files {
		files = append(files, entry.File)
	}
	manifest, digest, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(admission), files)
	if err != nil {
		t.Fatal("current collector inventory including an empty ancillary file", err)
	}
	publication.Manifest.SizeBytes, publication.Manifest.SHA256 = int64(len(manifest)), digest
	for _, mode := range []string{"negative file", "wrong empty digest", "empty report", "empty manifest", "empty bundle"} {
		invalid := publication
		invalid.Files = append([]biz.PublishedRuntimeFile{}, publication.Files...)
		switch mode {
		case "negative file":
			invalid.Files[len(invalid.Files)-1].Object.SizeBytes = -1
		case "wrong empty digest":
			invalid.Files[len(invalid.Files)-1].Object.SHA256 = strings.Repeat("4", 64)
		case "empty report":
			invalid.Files[0].File.SizeBytes, invalid.Files[0].File.SHA256 = 0, cpup01.EmptyContentSHA256
			invalid.Files[0].Object.SizeBytes, invalid.Files[0].Object.SHA256 = 0, cpup01.EmptyContentSHA256
			invalidFiles := append([]cpup01.OutputFile{}, files...)
			invalidFiles[0] = invalid.Files[0].File
			invalidManifest, invalidDigest, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(admission), invalidFiles)
			if err != nil {
				t.Fatal("empty report inventory", err)
			}
			invalid.Manifest.SizeBytes, invalid.Manifest.SHA256 = int64(len(invalidManifest)), invalidDigest
		case "empty manifest":
			invalid.Manifest.SizeBytes, invalid.Manifest.SHA256 = 0, cpup01.EmptyContentSHA256
		case "empty bundle":
			bundle := *publication.Bundle
			bundle.SizeBytes, bundle.SHA256 = 0, cpup01.EmptyContentSHA256
			invalid.Bundle = &bundle
		}
		if _, _, err := recordPublicationV21(t, repository, ctx, authority, invalid); !errors.Is(err, biz.ErrRuntimeConflict) {
			t.Fatalf("%s unexpectedly admitted: %v", mode, err)
		}
		if state, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID); err != nil || state.Publication != nil {
			t.Fatalf("%s persisted an invalid publication: %v", mode, err)
		}
	}
	state, replayed, err := recordPublicationV21(t, repository, ctx, authority, publication)
	if err != nil || replayed || state.Publication == nil {
		t.Fatalf("current empty ancillary object could not be persisted: %v", err)
	}
	readback, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil || !reflect.DeepEqual(readback.Publication, state.Publication) {
		t.Fatalf("empty ancillary identity changed across PostgreSQL reconnect: %v", err)
	}
	if _, replayed, err := recordPublicationV21(t, lifecycle.New(open()), ctx, authority, publication); err != nil || !replayed {
		t.Fatalf("empty ancillary publication was not an immutable replay: %v", err)
	}
}
