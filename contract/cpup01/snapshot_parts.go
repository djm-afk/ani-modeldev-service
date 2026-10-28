package cpup01

import "strings"

// These finite checks are shared by installed software materials and immutable
// execution snapshots. They do not select current configuration or providers.

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
	if workspace.WorkSubpath != "work" {
		return "workspace work subpath"
	}
	subpaths := []string{workspace.InputSubpath, workspace.TrainingSubpath, workspace.ReportsSubpath, workspace.PublicationSubpath, workspace.WorkSubpath}
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

const OutputContractSchemaVersion = "ani.modeldev.output-contract.v3"

func invalidOutputContract(output OutputContract) string {
	return invalidOutputContractForKind("GENERAL_TRAINING", output)
}
func invalidOutputContractForKind(kind string, output OutputContract) string {
	if output.SchemaVersion != OutputContractSchemaVersion || output.DeliveryMode != "SAVE_ARTIFACTS" {
		return "output contract"
	}
	roles := map[string]string{}
	switch kind {
	case "GENERAL_TRAINING":
		if output.OutputKind != "CHECKPOINT" {
			return "output kind"
		}
		roles = map[string]string{"model.pt": "CHECKPOINT", "model_config.json": "MODEL_CONFIG", "metrics.jsonl": "METRICS", "summary.json": "SUMMARY"}
	case "FINETUNING":
		if output.OutputKind != "MERGED_HF_MODEL" || !output.CreateTarBundle {
			return "output kind"
		}
		roles = map[string]string{"reports/train/metrics.jsonl": "METRICS", "reports/train/summary.json": "SUMMARY", "reports/train/adapter_manifest.json": "SOURCE_METADATA", "reports/train/effective-template.json": "SOURCE_METADATA", "reports/train/effective-template.jinja": "SOURCE_METADATA", "reports/export/merged_manifest.json": "EXPORT_REPORT", "reports/export/export.json": "EXPORT_REPORT", "reports/verify/verify.json": "VERIFY_REPORT"}
	default:
		return "output kind"
	}
	if len(output.RequiredFiles) != len(roles) {
		return "fixed platform reports"
	}
	for _, file := range output.RequiredFiles {
		role, ok := roles[file.RelativePath]
		if !ok || role != file.Role || file.InTar != (kind == "GENERAL_TRAINING") {
			return "fixed platform report"
		}
		delete(roles, file.RelativePath)
	}
	return ""
}
