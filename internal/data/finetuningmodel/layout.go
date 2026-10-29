package finetuningmodel

import (
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// NormalizeWeightLayout is shared by source preparation and output collection.
// Callers supply measured members and the actual bounded index bytes.
func NormalizeWeightLayout(files []cpup01.BaseModelFile, indexJSON []byte) (cpup01.WeightLayout, error) {
	observed := make([]*modelv1.FinetuningArtifactFile, 0, len(files))
	for _, file := range files {
		observed = append(observed, &modelv1.FinetuningArtifactFile{Path: file.Path, SizeBytes: file.SizeBytes, Sha256: file.SHA256})
	}
	layout, err := modelv1.InspectFinetuningWeightLayout(observed, indexJSON)
	if err != nil {
		return cpup01.WeightLayout{}, err
	}
	shards := append([]string{}, layout.ShardPaths...)
	return cpup01.WeightLayout{Kind: layout.Kind, EntryPath: layout.EntryPath, ShardPaths: shards}, nil
}
