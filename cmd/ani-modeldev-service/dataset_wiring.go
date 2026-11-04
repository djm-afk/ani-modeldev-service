package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/datasetprovider"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

// datasetNamespaceAdapter converts the registry's identity into the narrow one
// the dataset target reader consumes, so the dataset package never imports the
// registry model.
type datasetNamespaceAdapter struct{ platform *tenantenvironment.Platform }

func (adapter datasetNamespaceAdapter) ReadNamespaceIdentity(ctx context.Context, tenantID, originalUID string) (dataset.NamespaceIdentity, error) {
	cluster, name, uid, err := adapter.platform.ReadNamespaceIdentityParts(ctx, tenantID, originalUID)
	if err != nil {
		return dataset.NamespaceIdentity{}, err
	}
	return dataset.NamespaceIdentity{ClusterID: cluster, Name: name, UID: uid}, nil
}

// buildDatasetService assembles the dataset handler from the pieces the runtime
// already owns. It returns nil when import configuration is absent, which leaves
// the RPCs unreachable instead of half-working.
func buildDatasetService(pool *pgxpool.Pool, platform *tenantenvironment.Platform, registries *tenantenvironment.Repository, clusterID string, importConfig *confDatasetImport, limit func() (int64, error)) (modeldevv1.ModelDevDatasetServiceServer, error) {
	if importConfig == nil || platform == nil || registries == nil || pool == nil {
		return nil, nil
	}
	provider, err := datasetprovider.New(datasetprovider.Config{
		HuggingFaceEndpoint: importConfig.huggingFace,
		ModelScopeEndpoint:  importConfig.modelScope,
		Timeout:             importConfig.timeout,
	})
	if err != nil {
		return nil, err
	}
	observer, err := admissionfacts.NewObserver(registries)
	if err != nil {
		return nil, err
	}
	repo := dataset.New(pool).WithInputPublisher(input.New(pool))
	collections, err := biz.NewDatasetCollections(biz.DatasetCollectionsDeps{
		Datasets: repo, Commands: repo, Operations: repo, Versions: repo, Tasks: repo,
	})
	if err != nil {
		return nil, err
	}
	targets, err := dataset.NewTargetReader(datasetNamespaceAdapter{platform: platform}, observer, platform.PlatformInputScope(), clusterID)
	if err != nil {
		return nil, err
	}
	imports, err := biz.NewDatasetImports(biz.DatasetImportsDeps{
		Datasets: repo, Commands: repo, Tasks: repo, Versions: repo, Operations: repo,
		Acceptance: repo, Publication: repo, Transitions: repo, Targets: targets, Sources: provider,
	})
	if err != nil {
		return nil, err
	}
	return service.NewDataset(collections, imports, provider, limit), nil
}

// confDatasetImport is the resolved import configuration. Keeping it a plain
// struct means the composition decides once, not each call site.
type confDatasetImport struct {
	huggingFace string
	modelScope  string
	timeout     time.Duration
	maxRunning  int
}

// datasetImportConfig resolves the optional configuration once. Absent fields
// keep the feature off rather than defaulting to a public endpoint.
func datasetImportConfig(runtime *conf.ManagedRuntime) *confDatasetImport {
	if runtime == nil || runtime.GetDatasetImport() == nil {
		return nil
	}
	value := runtime.GetDatasetImport()
	if value.GetHuggingfaceEndpoint() == "" || value.GetModelscopeEndpoint() == "" {
		return nil
	}
	timeout := time.Duration(value.GetProviderTimeoutSeconds()) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &confDatasetImport{
		huggingFace: value.GetHuggingfaceEndpoint(),
		modelScope:  value.GetModelscopeEndpoint(),
		timeout:     timeout,
		maxRunning:  int(value.GetMaxRunningImports()),
	}
}
