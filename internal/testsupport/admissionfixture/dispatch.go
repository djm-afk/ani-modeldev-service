package admissionfixture

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// DispatchBinding groups the original tenant environment and owner for tests
// that inspect tenant-scoped pending rows. Runtime configuration never uses it.
type DispatchBinding struct {
	TenantID    string
	Environment cpup01.EnvironmentBindingSnapshot
	Owner       biz.PipelineOwnerConfiguration
}
