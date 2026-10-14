package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type ownerConfig struct {
	TenantID            string         `json:"tenant_id"`
	NamespaceUID        string         `json:"namespace_uid"`
	ContextFile         string         `json:"context_file"`
	TokenFile           string         `json:"token_file"`
	Target              string         `json:"grpc_target"`
	ServerName          string         `json:"grpc_server_name"`
	CAFile              string         `json:"grpc_ca_file"`
	WorkspaceDirectory  string         `json:"workspace_directory"`
	PVCName             string         `json:"pvc_name"`
	InventoryFile       string         `json:"inventory_file"`
	CandidateFile       string         `json:"candidate_file"`
	TaskID              string         `json:"task_id"`
	PollIntervalSeconds int            `json:"poll_interval_seconds"`
	TimeoutSeconds      int            `json:"timeout_seconds"`
	S3                  *storageConfig `json:"s3,omitempty"`
}

type storageConfig struct {
	Endpoint                string `json:"endpoint"`
	Region                  string `json:"region"`
	CAFile                  string `json:"ca_file"`
	CredentialsFile         string `json:"credentials_file"`
	StreamingTimeoutSeconds int    `json:"streaming_timeout_seconds,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:]); err != nil {
		// Never print errors containing remote response bodies, credentials,
		// signed URLs or owner configuration. The exit code fails this KFP task.
		fmt.Fprintln(os.Stderr, failureDiagnostic(err))
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "workspace-name" {
		return stageFailure("bootstrap", writeWorkspaceName(args[1:]))
	}
	invocation, err := bootstrapInvocation(ctx, args, nil)
	if err != nil {
		return stageFailure("bootstrap", err)
	}
	step, config, claim := invocation.step, invocation.config, invocation.claim
	if invocation.candidateJSON != nil {
		// This input lives only in the control Pod's private temporary directory.
		// It does not require the execution PVC or share a path with publishers.
		directory, err := os.MkdirTemp("", "ani-modeldev-close-")
		if err != nil {
			return stageFailure("bootstrap", component.ErrConfiguration)
		}
		defer os.RemoveAll(directory)
		config.CandidateFile = filepath.Join(directory, "publication.json")
		if err := os.WriteFile(config.CandidateFile, []byte(*invocation.candidateJSON), 0600); err != nil {
			return stageFailure("bootstrap", component.ErrConfiguration)
		}
	}
	roots, err := trustedRoots(config.CAFile)
	if err != nil {
		return stageFailure("grpc-ca", err)
	}
	connection, err := grpc.NewClient(config.Target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: config.ServerName})))
	if err != nil {
		return stageFailure("grpc-ca", component.ErrConfiguration)
	}
	defer connection.Close()
	kube := invocation.kube
	if step == "prepare" && kube == nil {
		kube, err = inClusterKubernetes()
		if err != nil {
			return stageFailure("bootstrap", component.ErrConfiguration)
		}
	}
	stepClient := modeldevv1.NewModelDevStepServiceClient(connection)
	var objects *s3.Client
	var baseObjects *s3.Client
	if step == "prepare" || step == "publish" {
		provider, providerErr := stepStorageCredentials(stepClient, config.TenantID, config.TokenFile, claim)
		if providerErr != nil {
			return stageFailure("storage-client", providerErr)
		}
		objects, err = storageClient(config.S3, provider)
		if err != nil {
			return stageFailure("storage-client", err)
		}
	}
	if step == "prepare" {
		provider, providerErr := stepStorageCredentials(stepClient, config.TenantID, config.TokenFile, claim, true)
		if providerErr != nil {
			return stageFailure("storage-client", providerErr)
		}
		baseObjects, err = storageClient(config.S3, provider)
		if err != nil {
			return stageFailure("storage-client", err)
		}
	}
	runner, err := component.New(component.Config{TenantID: config.TenantID, Context: claim, TokenFile: config.TokenFile, WorkspaceDirectory: config.WorkspaceDirectory, PVCName: config.PVCName, InventoryFile: config.InventoryFile, CandidateFile: config.CandidateFile, CloseOnly: invocation.closeOnly, TaskID: config.TaskID, PollInterval: time.Duration(config.PollIntervalSeconds) * time.Second}, stepClient, kube, objects, baseObjects)
	if err != nil {
		return stageFailure("runner", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	return stageFailure("run", runner.Run(ctx, step))
}

func storageClient(config *storageConfig, provider aws.CredentialsProvider) (*s3.Client, error) {
	if config == nil || config.Region == "" || config.CredentialsFile != "" || provider == nil || config.StreamingTimeoutSeconds < 0 || config.StreamingTimeoutSeconds > 900 {
		return nil, component.ErrConfiguration
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, component.ErrConfiguration
	}
	roots, err := trustedRoots(config.CAFile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	streamingTimeout := 2 * time.Minute
	if config.StreamingTimeoutSeconds > 0 {
		streamingTimeout = time.Duration(config.StreamingTimeoutSeconds) * time.Second
		transport.ResponseHeaderTimeout = streamingTimeout
	}
	client := &http.Client{Transport: transport, Timeout: streamingTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s3.New(s3.Options{Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), Credentials: provider, UsePathStyle: true, HTTPClient: client, RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}), nil
}

func trustedRoots(name string) (*x509.CertPool, error) {
	data, err := readOwnerFile(name, 1<<20)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, component.ErrConfiguration
	}
	return roots, nil
}

func readOwnerFile(name string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, component.ErrConfiguration
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, component.ErrConfiguration
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, component.ErrConfiguration
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, component.ErrConfiguration
	}
	return data, nil
}

func readConfig(name string, value any) error {
	data, err := readOwnerFile(name, 64<<10)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return component.ErrConfiguration
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return component.ErrConfiguration
	}
	return nil
}
