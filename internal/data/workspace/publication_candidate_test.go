package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
)

func TestPublicationCandidateStaysOnPVCWhileKFPReceivesOnlyItsBoundedReference(t *testing.T) {
	directory, execution, _ := finetuningOutputWorkspace(t)
	output, err := workspace.Collect(context.Background(), directory, execution)
	if err != nil {
		t.Fatal(err)
	}
	_, client := newPipelineS3(t, execution, nil)
	uploaded, err := workspace.UploadOutput(context.Background(), client, execution, directory, output)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cpup01.PublicationCandidate{Schema: cpup01.PublicationCandidateSchemaVersion, TenantID: execution.TenantID, OperationID: execution.OperationID, ExecutionID: execution.ExecutionID, ExecutionSpecHash: execution.SpecHash, LogicalPublicationKey: uploaded.LogicalKey, ManifestObject: uploaded.Manifest, BundleObject: uploaded.Bundle, UploadTaskID: "actual-publish-task", UploadPodUID: "77777777-7777-4777-8777-777777777777"}
	for _, file := range uploaded.Files {
		candidate.Files = append(candidate.Files, cpup01.PublishedFileCandidate{File: file.File, Object: file.Object})
	}
	admission := cpup01.AdmissionEnvelope(execution.Admission)
	ref, err := workspace.StorePublicationCandidate(context.Background(), directory, admission, candidate)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := ref.Canonical()
	if err != nil || len(wire) > 1024 {
		t.Fatal("KFP reference exceeds public bound", err)
	}
	stored, err := os.ReadFile(filepath.Join(directory, cpup01.PublicationCandidateRelativePath))
	if err != nil || int64(len(stored)) != ref.SizeBytes || pipelineDigest(stored) != ref.SHA256 {
		t.Fatal("candidate content identity differs", err)
	}
	decoded, err := workspace.ReadPublicationCandidate(context.Background(), directory, admission, ref)
	if err != nil || len(decoded.Files) != len(output.Files) || decoded.UploadPodUID != candidate.UploadPodUID {
		t.Fatal("full candidate failed frozen identity readback", err)
	}
	replay, err := workspace.StorePublicationCandidate(context.Background(), directory, admission, candidate)
	if err != nil || replay != ref {
		t.Fatal("same publication retry changed frozen file", err)
	}
	bad := ref
	bad.SizeBytes++
	if _, err := workspace.ReadPublicationCandidate(context.Background(), directory, admission, bad); !errors.Is(err, biz.ErrInvalidWorkspaceOutput) {
		t.Fatal("wrong size accepted", err)
	}
	other := admission
	other.ExecutionID = "88888888-8888-4888-8888-888888888888"
	if _, err := workspace.ReadPublicationCandidate(context.Background(), directory, other, ref); err == nil {
		t.Fatal("cross execution candidate accepted")
	}
	stored[len(stored)/2] ^= 1
	if err := os.WriteFile(filepath.Join(directory, cpup01.PublicationCandidateRelativePath), stored, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadPublicationCandidate(context.Background(), directory, admission, ref); !errors.Is(err, biz.ErrInvalidWorkspaceOutput) {
		t.Fatal("changed candidate accepted", err)
	}
}
