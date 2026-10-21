package main

import (
	"errors"
	"path/filepath"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/accelerator"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"k8s.io/client-go/dynamic"
)

// assembleGPUOwner extends the existing close worker and Trainer observer.
// Absence leaves CPU execution independent of ACC/Gov release connections.
func assembleGPUOwner(runtime *conf.ManagedRuntime, kube dynamic.Interface, repository *lifecycle.Repository) (*biz.GPUReleaseReconciler, *trainer.OwnerAssertionSigner, func(), error) {
	config := runtime.GetGpuOwner()
	if config == nil {
		return nil, nil, func() {}, nil
	}
	if repository == nil || kube == nil || !filepath.IsAbs(config.GetOwnerPrivateKeyFile()) {
		return nil, nil, nil, errors.New("GPU owner material unavailable")
	}
	convert := func(value *conf.OwnerMTLSConnection) (accelerator.Config, error) {
		if value == nil || value.GetTimeout() == nil || value.GetTimeout().CheckValid() != nil {
			return accelerator.Config{}, errors.New("GPU owner connection unavailable")
		}
		for _, path := range []string{value.GetCaFile(), value.GetCertificateFile(), value.GetPrivateKeyFile()} {
			if !filepath.IsAbs(path) {
				return accelerator.Config{}, errors.New("GPU owner connection requires mounted absolute paths")
			}
		}
		return accelerator.Config{Endpoint: value.GetEndpoint(), CAFile: value.GetCaFile(), CertificateFile: value.GetCertificateFile(), PrivateKeyFile: value.GetPrivateKeyFile(), ServerName: value.GetServerName(), Timeout: value.GetTimeout().AsDuration()}, nil
	}
	accConfig, err := convert(config.GetAccelerator())
	if err != nil {
		return nil, nil, nil, err
	}
	govConfig, err := convert(config.GetGovernance())
	if err != nil {
		return nil, nil, nil, err
	}
	signer, err := trainer.NewOwnerAssertionSigner(kube, config.GetOwnerPrivateKeyFile())
	if err != nil {
		return nil, nil, nil, errors.New("GPU owner signing key unavailable")
	}
	observer, closeACC, err := accelerator.NewReleaseClient(accConfig)
	if err != nil {
		return nil, nil, nil, errors.New("GPU owner ACC connection invalid")
	}
	reporter, closeGov, err := accelerator.NewQuotaClient(govConfig)
	if err != nil {
		closeACC()
		return nil, nil, nil, errors.New("GPU owner Governance connection invalid")
	}
	reconciler, err := biz.NewGPUReleaseReconciler(repository, observer, reporter)
	if err != nil {
		closeGov()
		closeACC()
		return nil, nil, nil, err
	}
	return reconciler, signer, func() { closeGov(); closeACC() }, nil
}
