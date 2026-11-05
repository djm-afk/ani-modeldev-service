package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/dataset"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"k8s.io/client-go/dynamic"
)

// buildDatasetScheduler assembles the import scheduler. It returns nil when the
// feature is not configured, which leaves acceptance disabled as well: a task
// nobody scans is worse than a rejected request.
func buildDatasetScheduler(
	pool *pgxpool.Pool,
	kube dynamic.Interface,
	registries *tenantenvironment.Repository,
	verifier *objectstore.Verifier,
	issuer *storagecredentials.Issuer,
	platform *tenantenvironment.Platform,
	clusterID string,
	importConfig *confDatasetImport,
) (*biz.ImportScheduler, error) {
	// Absent configuration means the feature is off; that is a deliberate
	// deployment choice, not a fault.
	if importConfig == nil {
		return nil, nil
	}
	if importConfig.importerDigest == "" || importConfig.importerImage == "" ||
		pool == nil || kube == nil || verifier == nil || issuer == nil || platform == nil {
		// Configured but unwireable: silently skipping would leave accepted tasks
		// unclaimed forever, which is exactly the failure this loop exists to fix.
		return nil, fmt.Errorf("%w: dataset import scheduler requested but missing %s",
			biz.ErrImportWorkerConfiguration, missingSchedulerInput(importConfig, pool, kube, verifier, issuer, platform))
	}
	// The scheduler publishes finished imports, so it needs the same input
	// publisher the service path wires. Without it the publication transaction
	// fails closed with "repository or input publisher not configured".
	repo := dataset.New(pool).WithInputPublisher(input.New(pool)).WithConcurrencyLimit(importConfig.maxRunning)
	planner, err := biz.NewImportPlanPlanner(importConfig.importerDigest, importConfig.credentialFile, importConfig.scheduleTimeout, nil)
	if err != nil {
		return nil, err
	}
	workload, err := dataset.NewImporterWorkload(dataset.ImporterWorkloadConfig{
		ClusterID: clusterID, Image: importConfig.importerImage, ImageDigest: importConfig.importerDigest,
		ServiceAccount: importConfig.serviceAccount, StorageClass: importConfig.storageClass,
		CPURequest: importConfig.cpuRequest, CPULimit: importConfig.cpuLimit,
		MemoryRequest: importConfig.memoryRequest, MemoryLimit: importConfig.memoryLimit,
		PVCSize: importConfig.pvcSize, PlanTimeout: importConfig.scheduleTimeout,
		S3Endpoint: importConfig.s3Endpoint, S3Region: importConfig.s3Region,
		HuggingFaceURL: importConfig.huggingFace, ModelScopeURL: importConfig.modelScope,
		CredentialFile: importConfig.credentialFile,
	}, kube)
	if err != nil {
		return nil, err
	}
	reader, err := dataset.NewCandidateReader(importConfig.s3Client, verifier, importConfig.connectionID)
	if err != nil {
		return nil, err
	}
	// The scheduler needs the registry lookup directly: the workload's
	// environment-id label must name a real execution-registry row.
	observer, err := admissionfacts.NewObserver(registries)
	if err != nil {
		return nil, err
	}
	targets, err := dataset.NewTargetReader(datasetNamespaceAdapter{platform: platform}, observer, platform.PlatformInputScope(), clusterID)
	if err != nil {
		return nil, err
	}
	return biz.NewImportScheduler(biz.ImportSchedulerDeps{
		Repository: repo, Worker: repo, Planner: planner, Workloads: workload, Objects: reader,
		Issuer: issuer, Publication: repo, Operations: repo, Environments: targets,
		Owner: importConfig.owner, Interval: importConfig.interval, LeaseTTL: importConfig.leaseTTL,
		Capacity: importConfig.maxRunning, ImportTimeout: importConfig.scheduleTimeout,
		Logf: importConfig.logf,
	})
}

// datasetSchedulerLifetime runs the scheduler beside the existing workers with
// one shared cancellation. A scheduler failure is reported, not swallowed.
func datasetSchedulerLifetime(scheduler *biz.ImportScheduler) func(context.Context) error {
	if scheduler == nil {
		return nil
	}
	return scheduler.Run
}

var (
	_ = errors.Is
	_ = time.Second
)

// missingSchedulerInput names every absent input so one log line explains why
// the scheduler did not start.
func missingSchedulerInput(importConfig *confDatasetImport, pool *pgxpool.Pool, kube dynamic.Interface, verifier *objectstore.Verifier, issuer *storagecredentials.Issuer, platform *tenantenvironment.Platform) string {
	var missing []string
	if importConfig == nil || importConfig.importerImage == "" {
		missing = append(missing, "importer_image")
	}
	if importConfig == nil || importConfig.importerDigest == "" {
		missing = append(missing, "importer_image_digest")
	}
	if pool == nil {
		missing = append(missing, "database")
	}
	if kube == nil {
		missing = append(missing, "kubernetes client")
	}
	if verifier == nil {
		missing = append(missing, "object verifier")
	}
	if issuer == nil {
		missing = append(missing, "storage issuer")
	}
	if platform == nil {
		missing = append(missing, "tenant platform")
	}
	if len(missing) == 0 {
		return "unknown input"
	}
	return strings.Join(missing, ", ")
}
