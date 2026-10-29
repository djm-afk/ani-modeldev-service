package main

import (
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/finetuningmodel"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/modelclient"
)

// One composition owns one Model connection for both immutable source reads
// and original registration operations. No request creates another connection.
func loadModelResources(config *conf.GovernanceCommand, runtimeConfig *conf.ManagedRuntime) (*modelclient.Client, biz.FinetuningModelFactsReader, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, nil, noop, nil
	}
	if config.Model == nil {
		return nil, nil, noop, nil
	}
	limits, err := runtimeConfig.ContentLimits()
	if err != nil {
		return nil, nil, noop, errors.New("Model content policy required")
	}
	c := config.Model
	if c.BaseModelStorageScope == nil {
		return nil, nil, noop, errors.New("Model base storage scope required")
	}
	s := c.BaseModelStorageScope
	scope := cpup01.StorageScope{StorageConnectionID: s.ConnectionId, Bucket: s.Bucket, ApprovedPrefix: s.ApprovedPrefix, CredentialReference: s.CredentialReference}
	if scope.Validate() != nil {
		return nil, nil, noop, errors.New("invalid Model base storage scope")
	}
	client, closeClient, err := modelclient.New(modelclient.Config{Endpoint: c.Endpoint, CAFile: c.CaFile, ClientDNSName: c.ClientDnsName, CertificateFile: c.CertificateFile, PrivateKeyFile: c.PrivateKeyFile, ServerName: c.ServerName, Actor: c.Actor, Timeout: c.Timeout.AsDuration(), ResolutionTimeout: runtimeConfig.ModelResolutionTimeout()})
	if err != nil {
		return nil, nil, noop, errors.New("Model connection materials invalid or unavailable")
	}
	reader, err := finetuningmodel.NewLive(client, scope, limits)
	if err != nil {
		closeClient()
		return nil, nil, noop, errors.New("Model read dependency invalid")
	}
	return client, reader, closeClient, nil
}
