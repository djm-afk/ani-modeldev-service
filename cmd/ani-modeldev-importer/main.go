package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// resolvedFileURL builds the exact download URL for a pinned file. Both hubs
// serve raw file bytes at a path derived from (repo, revision, file); nothing is
// resolved by name, and no redirect is followed to another host.
func resolvedFileURL(plan ImportPlan, huggingFaceEndpoint, modelScopeEndpoint string) (string, error) {
	// The resolved revision is what pins the bytes when the lock is immutable.
	// A file-SHA lock still downloads through the requested revision and then
	// proves the bytes by digest.
	revision := plan.ResolvedRevision
	if revision == "" {
		revision = plan.RequestedRevisionOrResolved()
	}
	if revision == "" {
		return "", biz.ErrSourceNotPinnable
	}
	switch plan.Provider {
	case biz.ProviderHuggingFace:
		endpoint := strings.TrimSuffix(huggingFaceEndpoint, "/")
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return "", biz.ErrSourceInvalidInput
		}
		return endpoint + "/datasets/" + plan.RepoID + "/resolve/" + url.PathEscape(revision) + "/" + plan.FilePath, nil
	case biz.ProviderModelScope:
		endpoint := strings.TrimSuffix(modelScopeEndpoint, "/")
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return "", biz.ErrSourceInvalidInput
		}
		return endpoint + "/api/v1/datasets/" + plan.RepoID + "/repo?Revision=" + url.QueryEscape(revision) + "&FilePath=" + url.QueryEscape(plan.FilePath), nil
	}
	return "", biz.ErrSourceInvalidInput
}

// RequestedRevisionOrResolved returns the frozen requested revision, which the
// file-SHA lock downloads through when no commit id was proven.
func (plan ImportPlan) RequestedRevisionOrResolved() string { return plan.RequestedRevision }

// HTTPFetcher downloads the exact file with a bounded request and anonymous
// access only.
type HTTPFetcher struct {
	client   *http.Client
	hfHost   string
	msHost   string
	deadline time.Time
}

func NewHTTPFetcher(hfHost, msHost string, deadline time.Time) (*HTTPFetcher, error) {
	if hfHost == "" || msHost == "" {
		return nil, biz.ErrSourceInvalidInput
	}
	return &HTTPFetcher{
		client: &http.Client{
			Timeout:       30 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		hfHost: hfHost, msHost: msHost, deadline: deadline,
	}, nil
}

func (fetcher *HTTPFetcher) Stream(ctx context.Context, plan ImportPlan) (io.ReadCloser, error) {
	if !fetcher.deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, fetcher.deadline)
		_ = cancel // the body is closed by the caller; deadline still applies
	}
	target, err := resolvedFileURL(plan, fetcher.hfHost, fetcher.msHost)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, biz.ErrSourceInvalidInput
	}
	// Anonymous only: no Authorization, cookie or ambient credential.
	response, err := fetcher.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, biz.ErrSourceNotFound
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		response.Body.Close()
		return nil, biz.ErrSourceAuthRequired
	case http.StatusNotFound:
		response.Body.Close()
		return nil, biz.ErrSourceNotFound
	default:
		response.Body.Close()
		return nil, biz.ErrSourceNotFound
	}
	return response.Body, nil
}

// writeReport writes the result JSON to the preallocated result key through the
// uploader, then keeps a local copy for diagnostics.
func writeReport(ctx context.Context, plan ImportPlan, uploader ObjectUploader, report biz.ImportResult, localPath string) error {
	raw, err := biz.EncodeImportResult(report)
	if err != nil {
		return err
	}
	if localPath != "" {
		if err := os.WriteFile(localPath, raw, 0o600); err != nil {
			return err
		}
	}
	_, _, err = uploader.Put(ctx, plan.Result, strings.NewReader(string(raw)), int64(len(raw)))
	return err
}

func main() {
	var planPath, workDir, reportPath string
	flag.StringVar(&planPath, "plan", "", "path to the mounted import plan")
	flag.StringVar(&workDir, "work-dir", "/workspace/work/import", "scratch directory for the staged download")
	flag.StringVar(&reportPath, "report", "/workspace/reports/import/result.json", "local copy of the result report")
	flag.Parse()

	if err := run(planPath, workDir, reportPath); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(planPath, workDir, reportPath string) error {
	plan, err := LoadImportPlan(planPath)
	if err != nil {
		// Without a valid plan there is no identity to report against.
		return err
	}
	deadline, err := time.Parse("2006-01-02T15:04:05.000000Z", plan.Deadline)
	if err != nil {
		return ErrPlanInvalid
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	fetcher, err := NewHTTPFetcher(os.Getenv("ANI_HF_ENDPOINT"), os.Getenv("ANI_MS_ENDPOINT"), deadline)
	if err != nil {
		return err
	}
	client, err := newS3Client(os.Getenv("ANI_S3_ENDPOINT"), os.Getenv("ANI_S3_REGION"), plan.CredentialFile)
	if err != nil {
		return err
	}
	outcome := RunImport(ctx, plan, fetcher, &S3Uploader{client: client}, workDir)

	report := biz.ImportResult{
		TenantID: plan.TenantID, DatasetID: plan.DatasetID, TaskID: plan.TaskID,
		AttemptID: plan.AttemptID, InputVersionID: plan.InputVersionID,
		SourceSnapshotSHA256: plan.SourceSnapshotSHA256, RuntimePlanSHA256: plan.RuntimePlanSHA256,
		ImporterImageDigest: plan.ImporterImageDigest,
		NamespaceUID:        os.Getenv("ANI_NAMESPACE_UID"),
		JobUID:              os.Getenv("ANI_JOB_UID"),
		PodUID:              os.Getenv("ANI_POD_UID"),
		CompletedAt:         CompletedAt(time.Now()),
	}
	if outcome.Object.VersionID != "" {
		report.Status = biz.ImportResultSucceeded
		object := outcome.Object
		report.Object = &object
	} else {
		report.Status = biz.ImportResultFailed
		report.FailureCode = outcome.Failure
		report.FailureStage = outcome.Stage
	}
	if err := writeReport(ctx, plan, &S3Uploader{client: client}, report, reportPath); err != nil {
		return errors.New("report upload failed")
	}
	if report.Status != biz.ImportResultSucceeded {
		return errors.New("import failed: " + string(report.FailureCode))
	}
	return nil
}
