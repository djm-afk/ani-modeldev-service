package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestStorageStreamingTimeoutBoundsDelayedSignedPUTCommit(t *testing.T) {
	for _, test := range []struct {
		name    string
		seconds int
		success bool
	}{
		{name: "commit after thirty seconds within frozen streaming budget", seconds: 60, success: true},
		{name: "one second overall budget remains bounded", seconds: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload := []byte("synthetic signed PUT must await its actual storage commit")
			var requests atomic.Int32
			bodyRead := make(chan struct{}, 1)
			handlerDone := make(chan bool, 1)
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(body, payload) || r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
					t.Error("real SDK request lost signed conditional PUT or complete body")
					w.WriteHeader(http.StatusBadRequest)
					handlerDone <- false
					return
				}
				bodyRead <- struct{}{}
				timer := time.NewTimer(31 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
					w.Header().Set("x-amz-version-id", "synthetic-committed-version")
					w.WriteHeader(http.StatusOK)
					handlerDone <- true
				case <-r.Context().Done():
					handlerDone <- false
				}
			}))
			defer peer.Close()
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "synthetic-streaming-key", SecretAccessKey: "synthetic-streaming-secret", SessionToken: "synthetic-streaming-session"}, nil
			})
			client, err := storageClient(&storageConfig{Endpoint: peer.URL, Region: "us-east-1", CAFile: ca, StreamingTimeoutSeconds: test.seconds}, provider)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Options().HTTPClient.(*http.Client).CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			started := time.Now()
			result, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("synthetic-bucket"), Key: aws.String("approved/synthetic/model.safetensors"), Body: bytes.NewReader(payload), ContentLength: aws.Int64(int64(len(payload))), IfNoneMatch: aws.String("*")})
			elapsed := time.Since(started)
			select {
			case <-bodyRead:
			default:
				t.Fatal("server did not consume original signed body")
			}
			var committed bool
			select {
			case committed = <-handlerDone:
			case <-time.After(2 * time.Second):
				t.Fatal("request handler did not finish after response or client cancellation")
			}
			if requests.Load() != 1 {
				t.Fatal("PUT was retried")
			}
			if test.success {
				if err != nil || result == nil || aws.ToString(result.VersionId) != "synthetic-committed-version" || !committed || elapsed < 31*time.Second || elapsed >= 60*time.Second {
					t.Fatalf("signed PUT did not await actual commit within 60s budget: elapsed=%s committed=%t err=%v", elapsed, committed, err)
				}
			} else {
				var timeout net.Error
				if err == nil || (!errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &timeout) || !timeout.Timeout())) || committed || elapsed < 900*time.Millisecond || elapsed > 4*time.Second {
					t.Fatalf("1s overall streaming deadline did not cancel pending commit: elapsed=%s committed=%t err=%v", elapsed, committed, err)
				}
			}
			t.Logf("signed PUT requests=1 body_complete=true streaming_budget=%ds elapsed=%s committed=%t handler_finished=true", test.seconds, elapsed, committed)
		})
	}
}
