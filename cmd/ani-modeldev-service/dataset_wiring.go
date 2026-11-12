package main

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

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
	if importConfig.maxRunning > 0 {
		repo.WithConcurrencyLimit(importConfig.maxRunning)
	}
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

	importerImage   string
	importerDigest  string
	serviceAccount  string
	storageClass    string
	cpuRequest      string
	cpuLimit        string
	memoryRequest   string
	memoryLimit     string
	pvcSize         string
	credentialFile  string
	scheduleTimeout time.Duration
	interval        time.Duration
	leaseTTL        time.Duration
	batchSize       int32
	owner           string
	connectionID    string
	s3Client        *s3.Client
	s3Endpoint      string
	s3Region        string
	logf            func(message string, args ...any)
}

// datasetImportConfig resolves the optional configuration once. Absent fields
// keep the feature off rather than defaulting to a public endpoint.
func datasetImportConfig(runtime *conf.ManagedRuntime, connectionID, s3Endpoint, s3Region string, s3Client *s3.Client, owner string) *confDatasetImport {
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
	interval := value.GetSchedulerInterval().AsDuration()
	if interval <= 0 {
		interval = time.Second
	}
	leaseTTL := value.GetSchedulerLeaseTtl().AsDuration()
	if leaseTTL <= 0 {
		leaseTTL = 2 * interval
	}
	return &confDatasetImport{
		huggingFace: value.GetHuggingfaceEndpoint(),
		modelScope:  value.GetModelscopeEndpoint(),
		timeout:     timeout,
		maxRunning:  int(value.GetMaxRunningImports()),

		importerImage:   value.GetImporterImage(),
		importerDigest:  value.GetImporterImageDigest(),
		serviceAccount:  value.GetImporterServiceAccount(),
		storageClass:    value.GetStorageClass(),
		cpuRequest:      value.GetCpuRequest(),
		cpuLimit:        value.GetCpuLimit(),
		memoryRequest:   value.GetMemoryRequest(),
		memoryLimit:     value.GetMemoryLimit(),
		pvcSize:         value.GetPvcSize(),
		credentialFile:  value.GetCredentialFile(),
		scheduleTimeout: time.Duration(runtime.GetImportTimeoutSeconds()) * time.Second,
		interval:        interval,
		leaseTTL:        leaseTTL,
		batchSize:       int32(value.GetSchedulerBatchSize()),
		owner:           owner,
		connectionID:    connectionID,
		s3Client:        s3Client,
		s3Endpoint:      s3Endpoint,
		s3Region:        s3Region,
	}
}
