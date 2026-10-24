package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrainedArtifactCopyReadsOriginalVersionAndSendsOneControlledPUT(t *testing.T) {
	for _, mode := range []string{"original", "wrong-host", "changed-source-bytes", "wrong-version", "forwarded-bearer"} {
		t.Run(mode, func(t *testing.T) {
			tenant := "11111111-1111-4111-8111-111111111111"
			payload := []byte("original verified publication bytes")
			digest := sha256.Sum256(payload)
			version := "original-immutable-version"
			var getCount, putCount atomic.Int32
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					getCount.Add(1)
					if r.URL.Path != "/publication/executions/"+tenant+"/model.tar" || r.URL.Query().Get("versionId") != version || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
						t.Error("source lost exact owner scope/version/SigV4")
						w.WriteHeader(403)
						return
					}
					actual := payload
					v := version
					if mode == "changed-source-bytes" {
						actual = bytes.Repeat([]byte("x"), len(payload))
					}
					if mode == "wrong-version" {
						v = "replacement-version"
					}
					w.Header().Set("x-amz-version-id", v)
					w.Header().Set("Content-Length", fmt.Sprint(len(actual)))
					_, _ = w.Write(actual)
				case http.MethodPut:
					putCount.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(body, payload) || r.URL.Path != "/models/"+tenant+"/trained-artifacts/model.tar" || r.URL.Query().Get("original") != "ephemeral-ticket" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.ContentLength != int64(len(payload)) {
						t.Error("controlled PUT changed bytes/scope or forwarded authentication")
						w.WriteHeader(400)
						return
					}
					w.WriteHeader(200)
				default:
					w.WriteHeader(405)
				}
			}))
			defer peer.Close()
			transport := peer.Client()
			transport.Timeout = time.Minute
			credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "synthetic-publication-key", SecretAccessKey: "synthetic-publication-secret"}, nil
			})
			store := s3.New(s3.Options{Region: "test-region", BaseEndpoint: aws.String(peer.URL), UsePathStyle: true, HTTPClient: transport, Credentials: credentials, RetryMaxAttempts: 1})
			source := NewVerifier(store, "fixed-store", 1024)
			copier, err := NewTrainedArtifactCopier(source, transport, peer.URL, "models")
			if err != nil {
				t.Fatal(err)
			}
			scope := cpup01.StorageScope{StorageConnectionID: "fixed-store", Bucket: "publication", ApprovedPrefix: "executions/" + tenant}
			object := cpup01.FixedObjectRef{StorageConnectionID: "fixed-store", Bucket: scope.Bucket, Key: scope.ApprovedPrefix + "/model.tar", VersionID: &version, SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
			execution := biz.Execution{Admission: biz.Admission{TenantID: tenant, Snapshot: cpup01.Snapshot{PublicationScope: scope, Finetuning: &cpup01.FinetuningSnapshot{}}}}
			grant := biz.TrainedArtifactUploadGrant{Session: biz.TrainedArtifactSession{RegistrationID: "22222222-2222-4222-8222-222222222222", StoragePath: tenant + "/trained-artifacts/model.tar"}, URL: peer.URL + "/models/" + tenant + "/trained-artifacts/model.tar?original=ephemeral-ticket", ExpiresAt: time.Now().Add(time.Minute)}
			if mode == "wrong-host" {
				grant.URL = "https://other.example.test/models/" + grant.Session.StoragePath + "?original=ephemeral-ticket"
			}
			if mode == "forwarded-bearer" {
				grant.Headers = map[string]string{"Authorization": "Bearer synthetic-do-not-forward"}
			}
			err = copier.CopyTrainedArtifact(context.Background(), execution, object, grant)
			if mode == "original" {
				if err != nil || getCount.Load() != 2 || putCount.Load() != 1 {
					t.Fatal("original transfer did not verify and send exactly once", getCount.Load(), putCount.Load(), err)
				}
			} else if err == nil || putCount.Load() != 0 {
				t.Fatal("invalid source/ticket reached Model PUT", mode, getCount.Load(), putCount.Load(), err)
			}
		})
	}
}
