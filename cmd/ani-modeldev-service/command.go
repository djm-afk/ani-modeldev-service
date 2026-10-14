package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
)

// newCursorCodec decodes the base64 signing key and builds the list-cursor
// codec. Presence of the command listener already implies validation passed, so
// a failure here is a startup error rather than a silent unsigned fallback.
func newCursorCodec(encoded string) (*biz.CursorCodec, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("cursor signing key unavailable")
	}
	codec, err := biz.NewCursorCodec(key)
	if err != nil {
		return nil, errors.New("cursor signing key invalid")
	}
	return codec, nil
}

// Command assembly consumes mounted references only. It does not run schema
// migrations, create roles, enable readiness, or fall back to another listener.
func buildCommandServer(listener *conf.Server_GRPC, config *conf.GovernanceCommand, codec *biz.CursorCodec, middlewares ...middleware.Middleware) (*kratosgrpc.Server, func(), error) {
	pool, err := openCommandPool(config)
	if err != nil {
		return nil, nil, err
	}
	s, err := buildCommandServerWithPool(listener, config, pool, codec, middlewares...)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return s, pool.Close, nil
}

// The runtime composition shares this restricted pool with all durable ports;
// this builder never takes ownership of the caller's pool.
func buildCommandServerWithPool(listener *conf.Server_GRPC, config *conf.GovernanceCommand, pool *pgxpool.Pool, codec *biz.CursorCodec, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	return buildCommandServerWithQuery(listener, config, pool, nil, codec, middlewares...)
}

func buildCommandServerWithQuery(listener *conf.Server_GRPC, config *conf.GovernanceCommand, pool *pgxpool.Pool, query modeldevv1.ModelDevQueryServiceServer, codec *biz.CursorCodec, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	return buildCommandServerWithCapabilities(listener, config, pool, query, nil, nil, codec, middlewares...)
}

func buildCommandServerWithCapabilities(listener *conf.Server_GRPC, config *conf.GovernanceCommand, pool *pgxpool.Pool, query modeldevv1.ModelDevQueryServiceServer, verifier biz.CSVVerifier, operations modeldevv1.ModelDevOperationsServiceServer, codec *biz.CursorCodec, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	failed := func(message string) (*kratosgrpc.Server, error) { return nil, errors.New(message) }
	ca, err := readCommandMaterial(config.ClientCaFile, 1<<20)
	if err != nil {
		return failed("command CA material unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return failed("command CA material invalid")
	}
	certificatePEM, err := readCommandMaterial(config.CertificateFile, 1<<20)
	if err != nil {
		return failed("command server certificate unavailable")
	}
	privateKeyPEM, err := readCommandMaterial(config.PrivateKeyFile, 1<<20)
	if err != nil {
		return failed("command server key unavailable")
	}
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return failed("command server certificate pair invalid")
	}
	// Reject a pair that does not present the registered server identity at
	// startup, so a mismatched SAN cannot silently fail every Governance call.
	if !server.ValidCommandServerCertificate(certificate) {
		return failed("command server certificate identity invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if codec == nil {
		return failed("query listener requires a cursor signing key")
	}
	command := service.NewCommand(execution.New(pool))
	var admission modeldevv1.ModelDevAdmissionServiceServer
	var management modeldevv1.ModelDevManagementServiceServer
	if resolution := config.AdmissionResolution; resolution != nil {
		sources := make([]admissionfacts.FileSource, len(resolution.FactsFiles))
		for i, file := range resolution.FactsFiles {
			sources[i] = admissionfacts.FileSource{Path: file.GetPath(), SHA256: file.GetSha256()}
		}
		facts, err := admissionfacts.Load(ctx, sources)
		if err != nil {
			return failed("command admission facts unavailable or invalid")
		}
		releases := catalogue.NewReader(resolution.CatalogueDirectory)
		if err := releases.Check(ctx); err != nil {
			return failed("command admission catalogue unavailable or invalid")
		}
		inputs := input.New(pool)
		admission = service.NewAdmission(biz.NewManagedAdmissionResolver(releases, inputs, facts))
		materials := biz.NewManagedMaterials(releases, facts, inputs, biz.NewInputImporter(inputs, verifier))
		management = service.NewManagement(materials)
		if query == nil {
			query = service.NewQuery(execution.New(pool), nil).WithMaterials(materials)
		} else if concrete, ok := query.(*service.Query); ok {
			concrete.WithMaterials(materials)
		}
	}
	// Every assembled query serves signed cursors; wire the codec regardless of
	// whether the admission resolution branch created the query. A nil query
	// still means "no query listener" and is left untouched.
	if concrete, ok := query.(*service.Query); ok {
		concrete.WithCursorCodec(codec)
	}
	s, err := server.NewGovernanceServicesServer(listener, server.CommandTLS{Certificate: certificate, ClientCAs: roots, GovernanceDNSName: config.GovernanceDnsName}, command, admission, query, server.GovernanceServices{Management: management, Operations: operations}, middlewares...)
	if err != nil {
		return failed("command listener configuration invalid")
	}
	return s, nil
}

func openCommandPool(config *conf.GovernanceCommand) (*pgxpool.Pool, error) {
	databaseBytes, err := readCommandMaterial(config.DatabaseUrlFile, 16<<10)
	if err != nil {
		return nil, errors.New("command database reference unavailable")
	}
	connection := strings.TrimSpace(string(databaseBytes))
	if connection == "" {
		return nil, errors.New("command database configuration invalid")
	}
	poolConfig, err := pgxpool.ParseConfig(connection)
	if err != nil {
		return nil, errors.New("command database configuration invalid")
	}
	poolConfig.MaxConns = 4
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("command database unavailable")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("command database unavailable")
	}
	return pool, nil
}

func readCommandMaterial(path string, maximum int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("command material unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("command material unavailable")
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(value) == 0 || int64(len(value)) > maximum {
		return nil, errors.New("command material unavailable")
	}
	return value, nil
}
