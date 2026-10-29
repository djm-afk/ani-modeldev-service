// Package imageruntime is ModelDev's narrow Resource ImageRuntime adapter. It
// owns only transport and caller identity: it never decides whether an image is
// usable and never receives pull credentials.
package imageruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// ClientDNSName is the single DNS SAN this client certificate must present. The
// Resource runtime policy accepts exactly this identity.
const ClientDNSName = "ani-modeldev-service"

// Config is deployment-only connection material. No public request selects an
// endpoint, certificate or server identity.
type Config struct {
	Endpoint        string
	CAFile          string
	CertificateFile string
	PrivateKeyFile  string
	// ServerName is the exact DNS SAN required on the Resource runtime server.
	ServerName string
	Timeout    time.Duration
}

// Client is safe for concurrent use once constructed.
type Client struct {
	connection *grpc.ClientConn
	client     imagev1.ImageRuntimeServiceClient
	timeout    time.Duration
}

var (
	_ biz.TrainingImageResolver = (*Client)(nil)
	_ biz.TrainingImageEnsurer  = (*Client)(nil)
)

var fixedDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func exactIdentity(cert *x509.Certificate, want string) bool {
	return cert != nil && len(cert.DNSNames) == 1 && cert.DNSNames[0] == want && !strings.Contains(want, "*") &&
		len(cert.URIs) == 0 && len(cert.IPAddresses) == 0 && len(cert.EmailAddresses) == 0
}

func exactServerDNS(cert *x509.Certificate, want string) bool {
	if cert == nil {
		return false
	}
	matched := false
	for _, name := range cert.DNSNames {
		if strings.Contains(name, "*") {
			return false
		}
		matched = matched || name == want
	}
	return matched
}

func connection(config Config) (*grpc.ClientConn, error) {
	host, port, err := net.SplitHostPort(config.Endpoint)
	number, numberErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || strings.ContainsAny(host, "/\\ \t\r\n") || numberErr != nil || number == 0 ||
		config.ServerName == "" || strings.ContainsAny(config.ServerName, "*:/\\ \t\r\n") || config.ServerName == ClientDNSName ||
		config.Timeout <= 0 || config.Timeout > 30*time.Second {
		return nil, errors.New("invalid image runtime connection")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.PrivateKeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, errors.New("image runtime client certificate unavailable")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !exactIdentity(leaf, ClientDNSName) {
		return nil, errors.New("image runtime client certificate does not present the registered DNS identity")
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil || len(ca) > 2<<20 {
		return nil, errors.New("image runtime CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("image runtime CA contains no certificates")
	}
	serverName := config.ServerName
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   serverName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !exactServerDNS(state.PeerCertificates[0], serverName) {
				return errors.New("image runtime server identity mismatch")
			}
			return nil
		},
	}
	return grpc.NewClient(config.Endpoint, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
}

// New validates every material at startup so a mismatched identity fails closed
// instead of degrading to an unauthenticated or plaintext call.
func New(config Config) (*Client, func(), error) {
	connection, err := connection(config)
	if err != nil {
		return nil, nil, err
	}
	return &Client{connection: connection, client: imagev1.NewImageRuntimeServiceClient(connection), timeout: config.Timeout}, func() { _ = connection.Close() }, nil
}

// callContext replaces, rather than appends to, any inbound metadata. A caller
// can never forward a forged tenant, actor or user token to Resource.
func (c *Client) callContext(ctx context.Context, tenant string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.client == nil || c.timeout <= 0 {
		return nil, nil, biz.ErrRuntimeNotReady
	}
	parsed, err := uuid.Parse(tenant)
	if err != nil || parsed == uuid.Nil || parsed.String() != tenant {
		return nil, nil, biz.ErrInvalidAdmission
	}
	call, cancel := context.WithTimeout(ctx, c.timeout)
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-request-id", uuid.NewString()))
	return call, cancel, nil
}

// ResolveTrainingImage asks Resource for the authoritative resolution of one
// catalogue selection. It re-checks the answer so a mismatched tenant, image,
// platform or reference can never be frozen into an execution.
func (c *Client) ResolveTrainingImage(ctx context.Context, tenantID, imageID, scope string, target cpup01.TrainingPlatform) (biz.TrainingImageResolution, error) {
	if ctx == nil || !cpup01.ValidTrainingImageID(imageID) {
		return biz.TrainingImageResolution{}, biz.ErrInvalidAdmission
	}
	wireScope := imagev1.ImageScope_IMAGE_SCOPE_TENANT
	if scope == "platform" {
		wireScope = imagev1.ImageScope_IMAGE_SCOPE_PLATFORM
	} else if scope != "tenant" {
		return biz.TrainingImageResolution{}, biz.ErrInvalidAdmission
	}
	call, cancel, err := c.callContext(ctx, tenantID)
	if err != nil {
		return biz.TrainingImageResolution{}, err
	}
	defer cancel()
	resolved, err := c.client.ResolveImageForWorkload(call, &imagev1.ResolveImageForWorkloadRequest{
		TenantId: tenantID, ImageId: imageID, Scope: wireScope,
		TargetPlatform: &imagev1.ImagePlatform{Os: target.OS, Architecture: target.Architecture, Variant: target.Variant},
	})
	if err != nil {
		return biz.TrainingImageResolution{}, err
	}
	platform := resolved.GetSelectedPlatform()
	reference, digest := resolved.GetResolvedReference(), resolved.GetDigest()
	// registry_authority is Resource's authoritative value. It is never derived
	// from the reference: case, port and mirror aliases must not make the two
	// services disagree on the shared Secret hash input.
	registry := resolved.GetRegistryAuthority()
	if resolved.GetTenantId() != tenantID || resolved.GetImageId() != imageID || resolved.GetScope() != wireScope ||
		resolved.GetImageVersion() <= 0 ||
		!fixedDigest.MatchString(digest) || !strings.HasSuffix(reference, "@"+digest) ||
		registry == "" || registry != cpup01.NormalizeRegistryAuthority(registry) || !strings.HasPrefix(reference, registry+"/") ||
		platform.GetOs() != target.OS || platform.GetArchitecture() != target.Architecture || platform.GetVariant() != target.Variant {
		return biz.TrainingImageResolution{}, biz.ErrImplementationUnavailable
	}
	return biz.TrainingImageResolution{
		ImageID:           imageID,
		Scope:             scope,
		ImageVersion:      resolved.GetImageVersion(),
		Reference:         reference,
		Digest:            digest,
		RegistryAuthority: registry,
		Platform:          target,
	}, nil
}

// EnsurePullSecret asks Resource to write the shared tenant pull Secret and
// returns only the non-sensitive reference. ModelDev never writes a Secret and
// never receives credentials.
func (c *Client) EnsurePullSecret(ctx context.Context, tenantID, clusterID, namespace, namespaceUID, registry string) (string, error) {
	if ctx == nil {
		return "", biz.ErrRuntimeNotReady
	}
	call, cancel, err := c.callContext(ctx, tenantID)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, err := c.client.EnsureTenantPullSecret(call, &imagev1.EnsureTenantPullSecretRequest{
		TenantId: tenantID, ClusterId: clusterID, Namespace: namespace, NamespaceUid: namespaceUID, RegistryAuthority: registry,
	})
	if err != nil {
		return "", err
	}
	reference := response.GetSecretRef()
	if response.GetRegistryAuthority() != registry || reference == "" {
		return "", biz.ErrRuntimeConflict
	}
	return reference, nil
}
