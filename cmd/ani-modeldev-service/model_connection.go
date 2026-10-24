package main

import (
	"context"
	"errors"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/finetuningmodel"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/modelclient"
)

// One composition owns one Model connection for both immutable source reads
// and original registration operations. No request creates another connection.
func loadModelResources(config *conf.GovernanceCommand) (*modelclient.Client, biz.FinetuningModelFactsReader, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, nil, noop, nil
	}
	if config.Model != nil && config.ControlledModelFacts != nil {
		return nil, nil, noop, errors.New("live and controlled Model facts cannot be combined")
	}
	if config.Model == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		reader, err := buildFinetuningModelFacts(ctx, config)
		return nil, reader, noop, err
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
	client, closeClient, err := modelclient.New(modelclient.Config{Endpoint: c.Endpoint, CAFile: c.CaFile, ClientDNSName: c.ClientDnsName, CertificateFile: c.CertificateFile, PrivateKeyFile: c.PrivateKeyFile, ServerName: c.ServerName, Actor: c.Actor, Timeout: c.Timeout.AsDuration()})
	if err != nil {
		return nil, nil, noop, errors.New("Model connection materials invalid or unavailable")
	}
	reader, err := finetuningmodel.NewLive(client, scope)
	if err != nil {
		closeClient()
		return nil, nil, noop, errors.New("Model read dependency invalid")
	}
	return client, reader, closeClient, nil
}
