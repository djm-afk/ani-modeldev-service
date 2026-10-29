package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/proto"
)

func TestRuntimePreparedReportPreservesFrozenDerivedDatasetFacts(t *testing.T) {
	for _, snapshot := range []cpup01.Snapshot{conformance.SnapshotV1(), conformance.FinetuningSnapshotV1()} {
		t.Run(snapshot.Kind, func(t *testing.T) {
			wire, err := contractpb.EncodeSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			candidate := &modeldevv1.PreparedInputCandidate{
				Input: wire.Input,
				Workspace: &trainingv1.WorkspaceRef{Mode: trainingv1.WorkspaceMode_WORKSPACE_MODE_EXECUTION_PVC,
					WorkSubpath: snapshot.Workspace.WorkSubpath},
				PreparedManifest: &trainingv1.ManifestRef{RelativePath: "prepared-manifest.json", SizeBytes: 117, Sha256: strings.Repeat("a", 64)},
			}
			workspace, input, err := decodeRuntimePrepared(candidate)
			if err != nil || !reflect.DeepEqual(input, snapshot.Input) || workspace.WorkSubpath != snapshot.Workspace.WorkSubpath {
				t.Fatalf("prepared report lost immutable input facts: got %+v, want %+v, err %v", input, snapshot.Input, err)
			}
			missing := proto.Clone(candidate).(*modeldevv1.PreparedInputCandidate)
			missing.Input.PreparedDataset = nil
			if _, _, err := decodeRuntimePrepared(missing); !errors.Is(err, biz.ErrInvalidAdmission) {
				t.Fatalf("missing current derived dataset facts accepted: %v", err)
			}
		})
	}
}
