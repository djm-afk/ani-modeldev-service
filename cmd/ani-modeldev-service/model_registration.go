package main

import (
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/modelclient"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

// withModelRegistration wires the trained-artifact registration session onto
// the managed runtime. Every material is deployment-pinned: the connection
// identity, the artifact plan and the controlled-copy transport all come from
// configuration, never from a request. While this is absent, RegisterModel
// keeps its original boundary and records the frozen intent as unavailable
// instead of inventing a provider result.
func withModelRegistration(runtime *biz.ManagedRuntime, config *conf.ModelConnection, dispatches biz.FinetuningRegistrationDispatchRepository, clients runtimeClients, connectionID string) (*biz.ManagedRuntime, func(), error) {
	failed := func() (*biz.ManagedRuntime, func(), error) {
		return nil, nil, errors.New("model registration materials unavailable or invalid")
	}
	if runtime == nil || config == nil || clients.store == nil || connectionID == "" {
		return failed()
	}
	client, closeClient, err := modelclient.New(modelclient.Config{
		Endpoint:        config.Endpoint,
		CAFile:          config.CaFile,
		ClientDNSName:   config.ClientDnsName,
		CertificateFile: config.CertificateFile,
		PrivateKeyFile:  config.PrivateKeyFile,
		ServerName:      config.ServerName,
		Actor:           config.Actor,
		Timeout:         config.Timeout.AsDuration(),
	})
	if err != nil {
		return failed()
	}
	plan := artifactPlan(config.ArtifactPlan)
	orchestrator, err := biz.NewTrainedArtifactRegistrationOrchestrator(dispatches, client, objectstore.NewArtifactUploader(clients.store, connectionID))
	if err != nil {
		closeClient()
		return failed()
	}
	runtime, err = runtime.WithFinetuningRegistration(orchestrator, plan)
	if err != nil {
		closeClient()
		return failed()
	}
	return runtime, closeClient, nil
}

// artifactPlan copies the pinned plan into the registration layer's own type so
// later mutation of the configuration cannot change an in-flight session.
func artifactPlan(config *conf.ModelArtifactPlan) biz.FinetuningArtifactPlan {
	if config == nil {
		return biz.FinetuningArtifactPlan{}
	}
	return biz.FinetuningArtifactPlan{
		FileName:     config.GetFileName(),
		Format:       config.GetFormat(),
		DisplayName:  config.GetDisplayName(),
		Description:  config.GetDescription(),
		Capabilities: append([]string(nil), config.GetCapabilities()...),
	}
}
