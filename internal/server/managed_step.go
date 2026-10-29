package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewManagedStepServer exposes only managed Step RPCs on a dedicated TLS
// listener. Workload tokens are verified by the Step use case, never by the
// Governance certificate identity or forwarded metadata.
func NewManagedStepServer(c *conf.Server_GRPC, certificate tls.Certificate, step modeldevv1.ModelDevStepServiceServer, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	if c == nil || step == nil || !validManagedStepCertificate(certificate) {
		return nil, errors.New("valid managed Step TLS certificate and handler required")
	}
	s := kratosgrpc.NewServer(
		kratosgrpc.Options(grpc.MaxRecvMsgSize(cpup01.MaxControlRPCBytes), grpc.MaxSendMsgSize(cpup01.MaxControlRPCBytes)),
		kratosgrpc.Network(c.Network), kratosgrpc.Address(c.Addr),
		// RegisterModel carries the caller's finite original REGISTER window.
		// Apply the short transport budget per other method below, so Kratos'
		// outer envelope cannot truncate a permitted large-object operation.
		kratosgrpc.Timeout(0),
		kratosgrpc.TLSConfig(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}),
		kratosgrpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			switch info.FullMethod {
			case modeldevv1.ModelDevStepService_RegisterModel_FullMethodName:
				if _, finite := ctx.Deadline(); !finite {
					return nil, status.Error(codes.InvalidArgument, "managed registration requires a caller deadline")
				}
				return next(ctx, request)
			case modeldevv1.ModelDevStepService_BeginExecution_FullMethodName,
				modeldevv1.ModelDevStepService_BeginManagedStep_FullMethodName,
				modeldevv1.ModelDevStepService_GetExecutionConfiguration_FullMethodName,
				modeldevv1.ModelDevStepService_GetStorageCredentials_FullMethodName,
				modeldevv1.ModelDevStepService_EnsureTraining_FullMethodName,
				modeldevv1.ModelDevStepService_GetTrainingStatus_FullMethodName,
				modeldevv1.ModelDevStepService_ReportStepResult_FullMethodName,
				modeldevv1.ModelDevStepService_RequestExecutionClose_FullMethodName,
				modeldevv1.ModelDevStepService_BeginFinetuningStep_FullMethodName,
				modeldevv1.ModelDevStepService_ReportFinetuningStepResult_FullMethodName:
				if timeout := c.Timeout.AsDuration(); timeout > 0 {
					bounded, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()
					ctx = bounded
				}
				return next(ctx, request)
			default:
				return nil, status.Error(codes.PermissionDenied, "managed Step listener does not permit this method")
			}
		}),
		kratosgrpc.StreamInterceptor(func(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
			return status.Error(codes.PermissionDenied, "managed Step listener does not permit streaming")
		}),
		kratosgrpc.Middleware(middlewares...), kratosgrpc.DisableReflection(),
	)
	modeldevv1.RegisterModelDevStepServiceServer(s, step)
	return s, nil
}

func validManagedStepCertificate(certificate tls.Certificate) bool {
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return false
	}
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok {
		return false
	}
	publicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicKey, leaf.RawSubjectPublicKeyInfo) {
		return false
	}
	if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return false
	}
	if len(leaf.ExtKeyUsage) == 0 {
		return true
	}
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}
