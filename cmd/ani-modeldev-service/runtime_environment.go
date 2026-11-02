package main

import (
	"context"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
)

type dispatchEnvironment struct {
	repository *tenantenvironment.Repository
	platform   *tenantenvironment.Platform
	clusterID  string
}

func (e dispatchEnvironment) VerifyDispatchAdmission(ctx context.Context, admission biz.Admission) error {
	b, err := e.repository.ReadReadyBinding(ctx, admission.TenantID, e.clusterID)
	if err != nil {
		return err
	}
	raw, err := b.Canonical()
	if err != nil || b.Snapshot(projection.Digest(raw)) != admission.Snapshot.Environment {
		return biz.ErrAdmissionConflict
	}
	return e.platform.VerifyBinding(ctx, admission.TenantID, admission.Snapshot.Environment)
}
