package cpup01

import "strings"

// These field-level checks are shared by the immutable Release and the accepted
// Snapshot. They preserve Snapshot's existing field errors and do no resolution.
func invalidRuntimeRef(runtime RuntimeRef) string {
	if !validSnapshotDNSName(runtime.Name) || (runtime.Kind != "ClusterTrainingRuntime" && runtime.Kind != "TrainingRuntime") || runtime.APIGroup != "trainer.kubeflow.org" {
		return "runtime reference"
	}
	if len(runtime.TargetJobs) == 0 {
		return "runtime targets"
	}
	targets := make(map[string]bool, len(runtime.TargetJobs))
	for _, target := range runtime.TargetJobs {
		if !validSnapshotDNSLabel(target) || targets[target] {
			return "runtime targets"
		}
		targets[target] = true
	}
	return ""
}

func invalidCPUResources(resources CPUResources) string {
	if resources.Nodes != 1 || resources.ProcessesPerNode != 1 || resources.RequestMillicpu <= 0 || resources.LimitMillicpu < resources.RequestMillicpu || resources.RequestMemoryBytes <= 0 || resources.LimitMemoryBytes < resources.RequestMemoryBytes {
		return "CPU resources"
	}
	return ""
}

func invalidWorkspaceContract(workspace WorkspaceContract) string {
	if (workspace.Mode != "EXECUTION_PVC" && workspace.Mode != "KFP_RUN_WORKSPACE") || !validSnapshotDNSName(workspace.StorageClass) || workspace.CapacityBytes <= 0 {
		return "workspace contract"
	}
	subpaths := []string{workspace.InputSubpath, workspace.TrainingSubpath, workspace.ReportsSubpath, workspace.PublicationSubpath}
	for i, subpath := range subpaths {
		if !validSnapshotPath(subpath) {
			return "workspace subpath"
		}
		for _, other := range subpaths[:i] {
			if subpath == other || strings.HasPrefix(subpath, other+"/") || strings.HasPrefix(other, subpath+"/") {
				return "overlapping workspace scopes"
			}
		}
	}
	return ""
}

func invalidOutputContract(output OutputContract) string {
	if output.MaxTarBytes != nil {
		return "output contract"
	}
	if output.SchemaVersion != "ani.cpu.output.v1" || output.OutputKind != "CHECKPOINT" || output.DeliveryMode != "SAVE_ARTIFACTS" {
		return "output contract"
	}
	roles := map[string]string{"model.pt": "CHECKPOINT", "model_config.json": "MODEL_CONFIG", "metrics.jsonl": "METRICS", "summary.json": "SUMMARY"}
	if len(output.RequiredFiles) != len(roles) || output.MaxFileCount < uint32(len(roles)) || output.MaxTotalBytes <= 0 {
		return "required output bounds"
	}
	for _, file := range output.RequiredFiles {
		role, exists := roles[file.RelativePath]
		if !exists || role != file.Role || file.MaxSizeBytes <= 0 || file.MaxSizeBytes > output.MaxTotalBytes || file.InTar != nil {
			return "required output file"
		}
		delete(roles, file.RelativePath)
	}
	return ""
}

func invalidOutputContractForKind(kind string, output OutputContract) string {
	if kind == "GENERAL_TRAINING" {
		return invalidOutputContract(output)
	}
	if output.SchemaVersion != "ani.finetune.output.v1" || output.OutputKind != "MERGED_HF_MODEL" || output.DeliveryMode != "SAVE_ARTIFACTS" || !output.CreateTarBundle || output.MaxTarBytes == nil || *output.MaxTarBytes <= 0 {
		return "output contract"
	}
	if len(output.RequiredFiles) == 0 || len(output.RequiredFiles) > 256 || output.MaxFileCount < uint32(len(output.RequiredFiles)) || output.MaxFileCount > 256 || output.MaxTotalBytes <= 0 {
		return "required output bounds"
	}
	seen := map[string]bool{}
	roles := map[string]bool{}
	for _, file := range output.RequiredFiles {
		if !validSnapshotPath(file.RelativePath) || seen[file.RelativePath] || file.MaxSizeBytes <= 0 || file.MaxSizeBytes > output.MaxTotalBytes || file.InTar == nil {
			return "required output file"
		}
		seen[file.RelativePath] = true
		roles[file.Role] = true
		switch file.Role {
		case "HF_MODEL":
			if !strings.HasPrefix(file.RelativePath, "export/merged/") || !*file.InTar {
				return "required output file"
			}
		case "LORA_ADAPTER":
			if !strings.HasPrefix(file.RelativePath, "training/adapter/") || *file.InTar {
				return "required output file"
			}
		case "METRICS":
			if file.RelativePath != "reports/train/metrics.jsonl" || *file.InTar {
				return "required output file"
			}
		case "SUMMARY":
			if file.RelativePath != "reports/train/summary.json" || *file.InTar {
				return "required output file"
			}
		case "EXPORT_REPORT":
			if !strings.HasPrefix(file.RelativePath, "reports/export/") || *file.InTar {
				return "required output file"
			}
		case "VERIFY_REPORT":
			if !strings.HasPrefix(file.RelativePath, "reports/verify/") || *file.InTar {
				return "required output file"
			}
		case "SOURCE_METADATA":
			if !strings.HasPrefix(file.RelativePath, "reports/train/") || *file.InTar {
				return "required output file"
			}
		default:
			return "required output file"
		}
	}
	for _, role := range []string{"HF_MODEL", "LORA_ADAPTER", "METRICS", "SUMMARY", "EXPORT_REPORT", "VERIFY_REPORT"} {
		if !roles[role] {
			return "required output file"
		}
	}
	return ""
}
