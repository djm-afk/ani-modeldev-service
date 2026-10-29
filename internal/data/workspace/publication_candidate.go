package workspace

import (
	"context"
	"io"
	"os"
	"path"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"golang.org/x/sys/unix"
)

// StorePublicationCandidate publishes the complete immutable candidate on the
// execution PVC. The only value returned to KFP is its bounded content identity.
func StorePublicationCandidate(ctx context.Context, directory string, admission cpup01.AdmissionEnvelope, candidate cpup01.PublicationCandidate) (cpup01.PublicationRef, error) {
	if err := candidate.ValidateFor(admission); err != nil {
		return cpup01.PublicationRef{}, biz.ErrInvalidWorkspaceOutput
	}
	raw, err := candidate.Canonical()
	if err != nil {
		return cpup01.PublicationRef{}, biz.ErrInvalidWorkspaceOutput
	}
	root, err := openPipelineRoot(directory)
	if err != nil {
		return cpup01.PublicationRef{}, err
	}
	defer unix.Close(root)
	publication, err := openPipelineSubdirectory(root, path.Dir(cpup01.PublicationCandidateRelativePath))
	if err != nil {
		return cpup01.PublicationRef{}, err
	}
	defer unix.Close(publication)
	if err := writePipelineFile(ctx, publication, path.Base(cpup01.PublicationCandidateRelativePath), raw); err != nil {
		return cpup01.PublicationRef{}, err
	}
	return cpup01.PublicationRef{Schema: cpup01.PublicationRefSchemaVersion, SizeBytes: int64(len(raw)), SHA256: pipelineContentHash(raw)}, nil
}

// ReadPublicationCandidate never accepts a path from KFP. Its caller mounts the
// bound execution PVC read-only; the owner independently proves that Pod binding.
func ReadPublicationCandidate(ctx context.Context, directory string, admission cpup01.AdmissionEnvelope, ref cpup01.PublicationRef) (cpup01.PublicationCandidate, error) {
	if _, err := ref.Canonical(); err != nil {
		return cpup01.PublicationCandidate{}, biz.ErrInvalidWorkspaceOutput
	}
	root, err := openPipelineRoot(directory)
	if err != nil {
		return cpup01.PublicationCandidate{}, err
	}
	defer unix.Close(root)
	fd, err := openPipelineReadFile(root, cpup01.PublicationCandidateRelativePath)
	if err != nil {
		return cpup01.PublicationCandidate{}, err
	}
	file := os.NewFile(uintptr(fd), cpup01.PublicationCandidateRelativePath)
	defer file.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != ref.SizeBytes {
		return cpup01.PublicationCandidate{}, biz.ErrInvalidWorkspaceOutput
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, ref.SizeBytes))
	var extra [1]byte
	n, probeErr := file.Read(extra[:])
	if ctx.Err() != nil {
		return cpup01.PublicationCandidate{}, ctx.Err()
	}
	if err != nil || n != 0 || probeErr != io.EOF || unix.Fstat(fd, &after) != nil || after.Nlink != 1 || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim {
		return cpup01.PublicationCandidate{}, biz.ErrInvalidWorkspaceOutput
	}
	candidate, err := cpup01.VerifyPublicationCandidate(ref, raw)
	if err != nil || candidate.ValidateFor(admission) != nil {
		return cpup01.PublicationCandidate{}, biz.ErrInvalidWorkspaceOutput
	}
	return candidate, nil
}
