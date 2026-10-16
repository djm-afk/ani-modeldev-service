// Package conf contains the generated and validated runtime configuration.
package conf

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

const maximumTimeout = 30 * time.Second

func (c *Bootstrap) Validate() error {
	if c == nil {
		return fmt.Errorf("bootstrap config is required")
	}
	if c.Server == nil || c.Server.Grpc == nil || c.Server.Admin == nil {
		return fmt.Errorf("grpc and admin server config are required")
	}
	if c.Command != nil {
		for _, reference := range []string{c.Command.DatabaseUrlFile, c.Command.ClientCaFile, c.Command.CertificateFile, c.Command.PrivateKeyFile} {
			if !filepath.IsAbs(reference) || filepath.Clean(reference) != reference {
				return fmt.Errorf("command connection materials require absolute file references")
			}
		}
		if len(c.Command.GovernanceDnsName) > 253 || !commandDNSName.MatchString(c.Command.GovernanceDnsName) || c.Command.GovernanceDnsName == "unknown" {
			return fmt.Errorf("command requires an explicit exact Governance DNS identity")
		}
		if c.Command.AdmissionResolution != nil {
			if err := validateAdmissionResolution(c.Command.AdmissionResolution); err != nil {
				return err
			}
		}
		if c.Command.Model != nil {
			if err := validateModelConnection(c.Command.Model); err != nil {
				return err
			}
		}
	}
	if err := validateListener("grpc", c.Server.Grpc.Network, c.Server.Grpc.Addr, c.Server.Grpc.Timeout); err != nil {
		return err
	}
	if err := validateListener("admin", c.Server.Admin.Network, c.Server.Admin.Addr, c.Server.Admin.Timeout); err != nil {
		return err
	}
	_, grpcPortText, _ := net.SplitHostPort(c.Server.Grpc.Addr)
	_, adminPortText, _ := net.SplitHostPort(c.Server.Admin.Addr)
	grpcPort, _ := strconv.Atoi(grpcPortText)
	adminPort, _ := strconv.Atoi(adminPortText)
	if grpcPort == adminPort {
		return fmt.Errorf("grpc and admin listeners must use distinct ports")
	}
	if c.Runtime != nil {
		if c.Command == nil {
			return fmt.Errorf("managed runtime requires the durable command connection")
		}
		if err := validateManagedRuntime(c.Runtime, grpcPort, adminPort); err != nil {
			return err
		}
	}
	return validateDuration("shutdown", c.Server.ShutdownTimeout, maximumTimeout)
}

func validateManagedRuntime(config *ManagedRuntime, commandPort, adminPort int) error {
	if config.Step == nil || config.Kubernetes == nil || config.Pipeline == nil || config.ObjectStorage == nil {
		return fmt.Errorf("managed runtime requires step, Kubernetes, pipeline and storage configuration")
	}
	if err := validateBoundedListener("managed step", config.Step.Network, config.Step.Addr, config.Step.Timeout, 10*time.Minute); err != nil {
		return err
	}
	_, portText, _ := net.SplitHostPort(config.Step.Addr)
	port, _ := strconv.Atoi(portText)
	if port == commandPort || port == adminPort {
		return fmt.Errorf("managed step listener requires a separate port")
	}
	for _, reference := range []string{config.CertificateFile, config.PrivateKeyFile, config.BindingFile, config.Kubernetes.CaFile, config.Kubernetes.TokenFile, config.Pipeline.CaFile, config.Pipeline.TokenFile, config.ObjectStorage.CaFile, config.ObjectStorage.CredentialsFile} {
		if !filepath.IsAbs(reference) || filepath.Clean(reference) != reference || strings.ContainsRune(reference, 0) {
			return fmt.Errorf("managed runtime materials require absolute file references")
		}
	}
	for _, address := range []string{config.Kubernetes.Endpoint, config.Pipeline.Endpoint, config.ObjectStorage.Endpoint} {
		endpoint, err := url.Parse(address)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" || strings.TrimSpace(address) != address {
			return fmt.Errorf("managed runtime requires explicit HTTPS endpoints")
		}
	}
	if len(config.BindingSha256) != 64 || strings.Trim(config.BindingSha256, "0123456789abcdef") != "" || config.TokenAudience == "" || len(config.TokenAudience) > 2048 || strings.ContainsAny(config.TokenAudience, " \t\r\n") {
		return fmt.Errorf("managed runtime requires a pinned binding and token audience")
	}
	if config.ObjectStorage.ConnectionId == "" || config.ObjectStorage.Region == "" || config.ObjectStorage.MaxObjectBytes <= 0 {
		return fmt.Errorf("managed storage requires connection, region and a positive read limit")
	}
	if err := validateDuration("managed API", config.ApiTimeout, maximumTimeout); err != nil {
		return err
	}
	if config.ObjectStorage.StreamingTimeout != nil {
		if err := validateDuration("managed storage streaming", config.ObjectStorage.StreamingTimeout, 10*time.Minute); err != nil {
			return err
		}
	}
	if err := validateDuration("dispatch interval", config.DispatchInterval, time.Minute); err != nil {
		return err
	}
	if config.DispatchInterval.AsDuration() < 100*time.Millisecond || config.DispatchBatchSize == 0 || config.DispatchBatchSize > 100 {
		return fmt.Errorf("dispatch requires interval at least 100ms and batch size 1..100")
	}
	return nil
}

var commandDNSName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

// validateModelConnection accepts only a fully specified business mTLS
// dependency. A partially configured Model block is rejected rather than
// silently disabling FINETUNING after an operator intended to enable it.
func validateModelConnection(config *ModelConnection) error {
	for _, reference := range []string{config.CaFile, config.CertificateFile, config.PrivateKeyFile} {
		if !filepath.IsAbs(reference) || filepath.Clean(reference) != reference || strings.ContainsRune(reference, 0) {
			return fmt.Errorf("model connection materials require absolute file references")
		}
	}
	if config.Endpoint == "" || strings.TrimSpace(config.Endpoint) != config.Endpoint || strings.ContainsAny(config.Endpoint, "/\\ \t\r\n") {
		return fmt.Errorf("model connection requires an explicit host:port endpoint")
	}
	host, portText, err := net.SplitHostPort(config.Endpoint)
	if err != nil || host == "" {
		return fmt.Errorf("model connection endpoint: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("model connection requires a numeric port in 1..65535")
	}
	for name, value := range map[string]string{"client DNS identity": config.ClientDnsName, "server DNS identity": config.ServerName} {
		if len(value) > 253 || value == "unknown" || !commandDNSName.MatchString(value) {
			return fmt.Errorf("model connection requires an explicit exact %s", name)
		}
	}
	if config.ClientDnsName == config.ServerName {
		return fmt.Errorf("model client and server DNS identities must be distinct")
	}
	if config.Actor == "" || len(config.Actor) > 253 || strings.ContainsAny(config.Actor, " \t\r\n") {
		return fmt.Errorf("model connection requires a bounded audit actor")
	}
	return validateDuration("model connection", config.Timeout, maximumTimeout)
}

func validateAdmissionResolution(config *AdmissionResolution) error {
	validReference := func(path string) bool {
		return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
	}
	if !validReference(config.CatalogueDirectory) {
		return fmt.Errorf("admission catalogue requires an absolute directory reference")
	}
	if len(config.FactsFiles) == 0 || len(config.FactsFiles) > 64 {
		return fmt.Errorf("admission requires 1..64 explicitly pinned facts files")
	}
	for _, file := range config.FactsFiles {
		if file == nil || !validReference(file.Path) || len(file.Sha256) != 64 || strings.Trim(file.Sha256, "0123456789abcdef") != "" {
			return fmt.Errorf("admission facts require absolute file references and lowercase SHA256 pins")
		}
	}
	return nil
}

func validateListener(name, network, address string, timeout *durationpb.Duration) error {
	return validateBoundedListener(name, network, address, timeout, maximumTimeout)
}

func validateBoundedListener(name, network, address string, timeout *durationpb.Duration, maximum time.Duration) error {
	if network != "tcp" {
		return fmt.Errorf("%s network must be tcp", name)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s address: %w", name, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
		return fmt.Errorf("%s address must use a literal loopback or unspecified IP", name)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("%s address requires a numeric port in 1..65535", name)
	}
	return validateDuration(name, timeout, maximum)
}

func validateDuration(name string, value *durationpb.Duration, maximum time.Duration) error {
	if value == nil {
		return fmt.Errorf("%s timeout is required", name)
	}
	if err := value.CheckValid(); err != nil {
		return fmt.Errorf("%s timeout: %w", name, err)
	}
	duration := value.AsDuration()
	if duration <= 0 || duration > maximum {
		return fmt.Errorf("%s timeout must be within 1ns..%s", name, maximum)
	}
	return nil
}
