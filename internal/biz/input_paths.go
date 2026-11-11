package biz

import (
	"errors"
	"path"
	"strings"
)

// ErrInputKindFormatMismatch reports a READY input whose format the execution
// kind does not accept. It is distinct from ErrAdmissionInputNotReady: the
// data is ready, the combination is what is wrong, so operators must not be
// sent to look for a dataset-state problem.
var ErrInputKindFormatMismatch = errors.New("INPUT_KIND_FORMAT_MISMATCH")

// ErrInputPathUnavailable is a contract/integrity error: it means the frozen
// (kind, format) pair has no container path at all. It never reaches a caller
// as a business reason.
var ErrInputPathUnavailable = errors.New("ADMISSION_INPUT_PATH_UNAVAILABLE")

// ErrInputProofUnavailable means a READY input does not carry the proof that
// its own format requires. The current schema cannot produce this from legal
// data, so it is a persistence-integrity fault and must not be reported as a
// format mismatch or as "not ready".
var ErrInputProofUnavailable = errors.New("ADMISSION_INPUT_PROOF_UNAVAILABLE")

// inputLayout is the single definition of where a verified input lives inside
// a workload and where that workload may write. Container paths are fixed per
// implementation, so they are constants here rather than environment facts.
type inputLayout struct {
	inputFilePath       string
	outputDirectoryPath string
}

var inputLayouts = map[string]map[string]inputLayout{
	"GENERAL_TRAINING": {
		"CSV":  {inputFilePath: "/inputs/data.csv", outputDirectoryPath: "/outputs"},
		"JSON": {inputFilePath: "/inputs/data/finetune_data.jsonl", outputDirectoryPath: "/outputs"},
	},
	"FINETUNING": {
		"JSON": {inputFilePath: "/workspace/inputs/data/finetune_data.jsonl", outputDirectoryPath: "/workspace/training"},
	},
}

// AdmissionKindFormatSupported is the one legality judgement shared by the
// admission proof selection and the container path lookup. Two copies of this
// rule would let a pair pass one gate and fail the other.
func AdmissionKindFormatSupported(kind, format string) bool {
	formats, ok := inputLayouts[kind]
	if !ok {
		return false
	}
	_, ok = formats[format]
	return ok
}

// AdmissionInputPaths returns the frozen container paths for a legal
// (kind, format) pair and a contract error for anything else. It also applies
// the path safety rules, so a caller cannot skip them by using a new pair.
func AdmissionInputPaths(kind, format string) (inputFilePath, outputDirectoryPath string, err error) {
	formats, ok := inputLayouts[kind]
	if !ok {
		return "", "", ErrInputPathUnavailable
	}
	layout, ok := formats[format]
	if !ok {
		return "", "", ErrInputPathUnavailable
	}
	if !validAdmissionContainerPath(layout.inputFilePath) || !validAdmissionContainerPath(layout.outputDirectoryPath) ||
		layout.inputFilePath == layout.outputDirectoryPath ||
		strings.HasPrefix(layout.inputFilePath, layout.outputDirectoryPath+"/") ||
		strings.HasPrefix(layout.outputDirectoryPath, layout.inputFilePath+"/") {
		return "", "", ErrInputPathUnavailable
	}
	return layout.inputFilePath, layout.outputDirectoryPath, nil
}

func validAdmissionContainerPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && ValidStorageKey(strings.TrimPrefix(value, "/")) && !strings.ContainsAny(value, "*?[]")
}
