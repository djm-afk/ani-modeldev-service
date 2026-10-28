package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/imageruntime"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/traininglogs"
	"k8s.io/client-go/dynamic"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

type runtimeClients struct {
	certificate   tls.Certificate
	binding       biz.PipelineDispatchBinding
	kube          dynamic.Interface
	runs          *kfp.Client
	store         *s3.Client
	storageIssuer *storagecredentials.Issuer
	logs          *traininglogs.Reader
	// images is ModelDev's narrow Resource ImageRuntime client. It is nil when
	// the deployment declares no image runtime dependency.
	images *imageruntime.Client
	close  func()
}

func loadRuntimeClients(config *conf.ManagedRuntime, imageRuntime *conf.ImageRuntimeConnection) (runtimeClients, error) {
	failure := func() (runtimeClients, error) {
		return runtimeClients{}, errors.New("managed runtime materials unavailable or invalid")
	}
	bindingBytes, err := readCommandMaterial(config.BindingFile, 1<<20)
	if err != nil {
		return failure()
	}
	digest := sha256.Sum256(bindingBytes)
	if hex.EncodeToString(digest[:]) != config.BindingSha256 {
		return failure()
	}
	var binding biz.PipelineDispatchBinding
	if decodeRuntimeMaterial(bindingBytes, &binding) != nil || binding.Validate() != nil {
		return failure()
	}
	certificatePEM, err := readCommandMaterial(config.CertificateFile, 1<<20)
	if err != nil {
		return failure()
	}
	keyPEM, err := readCommandMaterial(config.PrivateKeyFile, 1<<20)
	if err != nil {
		return failure()
	}
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return failure()
	}
	kubeCA, err := runtimeRoots(config.Kubernetes.CaFile)
	if err != nil {
		return failure()
	}
	_ = kubeCA // Parse now; client-go consumes the same configured CA file.
	if _, err := runtimeToken(config.Kubernetes.TokenFile); err != nil {
		return failure()
	}
	provider := mountedPipelineToken{binding: binding, file: config.Pipeline.TokenFile}
	if _, err := provider.BearerToken(context.Background(), binding.TenantID, binding.Environment); err != nil {
		return failure()
	}
	pipelineCA, err := runtimeRoots(config.Pipeline.CaFile)
	if err != nil {
		return failure()
	}
	storageCA, err := runtimeRoots(config.ObjectStorage.CaFile)
	if err != nil {
		return failure()
	}
	credentials := mountedStorageCredentials(config.ObjectStorage.CredentialsFile)
	if _, err := credentials.Retrieve(context.Background()); err != nil {
		return failure()
	}
	storageMaterial, err := runtimeStorageMaterial(config.ObjectStorage.CredentialsFile)
	if err != nil {
		return failure()
	}
	issuer, err := storagecredentials.New(storagecredentials.Config{
		Endpoint: config.ObjectStorage.Endpoint, Region: config.ObjectStorage.Region, RootCAs: storageCA,
		Credentials: credentials, Bindings: storageMaterial.STSBindings, Timeout: config.ApiTimeout.AsDuration(),
	})
	if err != nil {
		return failure()
	}
	timeout := config.ApiTimeout.AsDuration()
	kubeConfig := &rest.Config{
		Host: config.Kubernetes.Endpoint, BearerTokenFile: config.Kubernetes.TokenFile,
		TLSClientConfig: rest.TLSClientConfig{CAFile: config.Kubernetes.CaFile}, Timeout: timeout,
		Proxy:     func(*http.Request) (*url.URL, error) { return nil, nil },
		UserAgent: "ani-modeldev-service", QPS: 5, Burst: 10,
	}
	kubeHTTP, err := rest.HTTPClientFor(kubeConfig)
	if err != nil {
		return failure()
	}
	kubeHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	kube, err := dynamic.NewForConfigAndClient(kubeConfig, kubeHTTP)
	if err != nil {
		kubeHTTP.CloseIdleConnections()
		return failure()
	}
	core, err := coreclient.NewForConfigAndClient(kubeConfig, kubeHTTP)
	if err != nil {
		kubeHTTP.CloseIdleConnections()
		return failure()
	}
	runs, err := kfp.New(kfp.Config{ConnectionRef: binding.Environment.KFPConnectionRef, Endpoint: config.Pipeline.Endpoint, RootCAs: pipelineCA, Timeout: timeout}, provider)
	if err != nil {
		kubeHTTP.CloseIdleConnections()
		return failure()
	}
	storageTransport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: storageCA, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxConnsPerHost: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	}
	streamingTimeout := timeout
	if config.ObjectStorage.StreamingTimeout != nil {
		streamingTimeout = config.ObjectStorage.StreamingTimeout.AsDuration()
	}
	storageHTTP := &http.Client{Transport: storageTransport, Timeout: streamingTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	store := s3.New(s3.Options{
		Region: config.ObjectStorage.Region, BaseEndpoint: aws.String(config.ObjectStorage.Endpoint), UsePathStyle: true,
		HTTPClient: storageHTTP, Credentials: credentials, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	images, closeImages, err := loadImageRuntime(imageRuntime)
	if err != nil {
		kubeHTTP.CloseIdleConnections()
		storageHTTP.CloseIdleConnections()
		issuer.Close()
		return failure()
	}
	return runtimeClients{certificate: certificate, binding: binding, kube: kube, runs: runs, store: store, storageIssuer: issuer, logs: traininglogs.New(core), images: images, close: func() { closeImages(); kubeHTTP.CloseIdleConnections(); storageHTTP.CloseIdleConnections(); issuer.Close() }}, nil
}

// loadImageRuntime requires every material when the dependency is declared, so
// a partially materialised identity fails startup instead of silently losing
// training image resolution or the shared pull Secret.
func loadImageRuntime(config *conf.ImageRuntimeConnection) (*imageruntime.Client, func(), error) {
	if config == nil {
		return nil, func() {}, nil
	}
	client, closeClient, err := imageruntime.New(imageruntime.Config{
		Endpoint: config.Endpoint, CAFile: config.CaFile, CertificateFile: config.CertificateFile,
		PrivateKeyFile: config.PrivateKeyFile, ServerName: config.ServerName, Timeout: config.Timeout.AsDuration(),
	})
	if err != nil {
		return nil, func() {}, err
	}
	return client, closeClient, nil
}

type mountedPipelineToken struct {
	binding biz.PipelineDispatchBinding
	file    string
}

func (provider mountedPipelineToken) BearerToken(ctx context.Context, tenant string, environment cpup01.EnvironmentBindingSnapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tenant != provider.binding.TenantID || environment != provider.binding.Environment {
		return "", errors.New("pipeline credential binding mismatch")
	}
	return runtimeToken(provider.file)
}

var runtimeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

func runtimeToken(file string) (string, error) {
	data, err := readCommandMaterial(file, 16<<10)
	if err != nil {
		return "", errors.New("runtime credential unavailable")
	}
	token := strings.TrimSpace(string(data))
	if !runtimeTokenPattern.MatchString(token) {
		return "", errors.New("runtime credential invalid")
	}
	return token, nil
}

func mountedStorageCredentials(file string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		if err := ctx.Err(); err != nil {
			return aws.Credentials{}, err
		}
		value, err := runtimeStorageMaterial(file)
		if err != nil {
			return aws.Credentials{}, errors.New("storage credential invalid")
		}
		// Read on every SDK retrieval so mounted rotation does not require an
		// application restart. Never consult ambient AWS environment variables.
		return aws.Credentials{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken, Source: "mounted-runtime-material"}, nil
	})
}

type storageMaterial struct {
	AccessKeyID     string                       `json:"access_key_id"`
	SecretAccessKey string                       `json:"secret_access_key"`
	SessionToken    string                       `json:"session_token,omitempty"`
	STSBindings     []storagecredentials.Binding `json:"sts_bindings"`
}

func runtimeStorageMaterial(file string) (storageMaterial, error) {
	data, err := readCommandMaterial(file, 64<<10)
	var value storageMaterial
	if err != nil || decodeRuntimeMaterial(data, &value) != nil || value.AccessKeyID == "" || value.SecretAccessKey == "" || value.SessionToken != "" || strings.ContainsAny(value.AccessKeyID+value.SecretAccessKey, " \t\r\n") {
		return storageMaterial{}, errors.New("storage issuer material invalid")
	}
	return value, nil
}

func runtimeRoots(file string) (*x509.CertPool, error) {
	data, err := readCommandMaterial(file, 1<<20)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("runtime CA invalid")
	}
	return roots, nil
}

func decodeRuntimeMaterial(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("runtime material invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("runtime material invalid")
	}
	return nil
}
