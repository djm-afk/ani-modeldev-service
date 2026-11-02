package admissionfixture

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	environment "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/tenantenvironment"
	"testing"
)

// EnsureEnvironment provisions via the real registry, Platform and Ensure use
// case. Only external Kubernetes/KFP/Resource boundaries are substituted.
func EnsureEnvironment(t testing.TB, pool *pgxpool.Pool, admission biz.Admission) biz.Admission {
	t.Helper()
	return environment.Ensure(t, pool, admission)
}
