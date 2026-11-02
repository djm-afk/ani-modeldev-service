package tenantenvironment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `environment_id::text,tenant_id::text,cluster_id::text,namespace_name,COALESCE(namespace_uid::text,''),state,phase,provision_spec_canonical,provision_spec_hash,binding_canonical,COALESCE(binding_digest,''),kfp_connection_ref,experiment_name,COALESCE(experiment_id::text,''),experiment_create_intent,resource_receipts,first_operation_id::text,last_operation_id::text,first_actor_id,last_actor_id,reason_code,reason_stage,row_version,created_at,updated_at,last_verified_at,ready_at`

func scan(row pgx.Row) (biz.TenantExecutionEnvironment, error) {
	var v biz.TenantExecutionEnvironment
	var intent, receipts []byte
	var verified, ready *time.Time
	err := row.Scan(&v.ID, &v.TenantID, &v.ClusterID, &v.NamespaceName, &v.NamespaceUID, &v.State, &v.Phase, &v.ProvisionSpecCanonical, &v.ProvisionSpecHash, &v.BindingCanonical, &v.BindingDigest, &v.KFPConnectionRef, &v.ExperimentName, &v.ExperimentID, &intent, &receipts, &v.FirstOperationID, &v.LastOperationID, &v.FirstActorID, &v.LastActorID, &v.ReasonCode, &v.ReasonStage, &v.RowVersion, &v.CreatedAt, &v.UpdatedAt, &verified, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, biz.ErrTenantEnvironmentNotRegistered
	}
	if err != nil {
		return v, biz.ErrTenantEnvironmentPersistence
	}
	if verified != nil {
		v.LastVerifiedAt = *verified
	}
	if ready != nil {
		v.ReadyAt = *ready
	}
	if len(intent) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(intent))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&v.ExperimentIntent) != nil || decoder.Decode(new(any)) != io.EOF {
			return v, biz.ErrTenantEnvironmentPersistence
		}
	}
	v.Receipts, err = biz.DecodeEnvironmentReceipts(receipts)
	if err != nil {
		return v, err
	}
	return v, nil
}
func (r *Repository) Get(ctx context.Context, tenant, cluster string) (biz.TenantExecutionEnvironment, error) {
	if r == nil || r.pool == nil || !biz.CanonicalEnvironmentUUID(tenant) || !biz.CanonicalEnvironmentUUID(cluster) {
		return biz.TenantExecutionEnvironment{}, biz.ErrTenantEnvironmentInvalid
	}
	return scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM modeldev_tenant_execution_environments WHERE tenant_id=$1::uuid AND cluster_id=$2::uuid`, tenant, cluster))
}
func (r *Repository) Register(ctx context.Context, v biz.TenantExecutionEnvironment) (biz.TenantExecutionEnvironment, error) {
	if r == nil || r.pool == nil {
		return v, biz.ErrTenantEnvironmentPersistence
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO modeldev_tenant_execution_environments(environment_id,tenant_id,cluster_id,namespace_name,state,phase,provision_spec_canonical,provision_spec_hash,kfp_connection_ref,experiment_name,first_operation_id,last_operation_id,first_actor_id,last_actor_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'PROVISIONING','NAMESPACE',$5,$6,$7,$8,$9::uuid,$9::uuid,$10,$10) ON CONFLICT(tenant_id,cluster_id) DO NOTHING`, v.ID, v.TenantID, v.ClusterID, v.NamespaceName, v.ProvisionSpecCanonical, v.ProvisionSpecHash, v.KFPConnectionRef, v.ExperimentName, v.FirstOperationID, v.FirstActorID)
	if err != nil {
		return v, biz.ErrTenantEnvironmentPersistence
	}
	return r.Get(ctx, v.TenantID, v.ClusterID)
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func (r *Repository) Save(ctx context.Context, v *biz.TenantExecutionEnvironment) error {
	receipts, err := json.Marshal(v.Receipts)
	if err != nil {
		return biz.ErrTenantEnvironmentPersistence
	}
	var intent any
	if v.ExperimentIntent != nil {
		raw, e := json.Marshal(v.ExperimentIntent)
		if e != nil {
			return biz.ErrTenantEnvironmentPersistence
		}
		intent = raw
	}
	var binding any
	if len(v.BindingCanonical) > 0 {
		binding = v.BindingCanonical
	}
	err = r.pool.QueryRow(ctx, `UPDATE modeldev_tenant_execution_environments SET namespace_uid=$4::uuid,state=$5,phase=$6,binding_canonical=$7,binding_digest=$8,experiment_id=$9::uuid,experiment_create_intent=$10::jsonb,resource_receipts=$11::jsonb,last_operation_id=$12::uuid,last_actor_id=$13,reason_code=$14,reason_stage=$15,row_version=row_version+1,updated_at=clock_timestamp(),last_verified_at=$16,ready_at=$17 WHERE tenant_id=$1::uuid AND cluster_id=$2::uuid AND row_version=$3 RETURNING row_version,updated_at`, v.TenantID, v.ClusterID, v.RowVersion, nullable(v.NamespaceUID), v.State, v.Phase, binding, nullable(v.BindingDigest), nullable(v.ExperimentID), intent, receipts, v.LastOperationID, v.LastActorID, v.ReasonCode, v.ReasonStage, nullableTime(v.LastVerifiedAt), nullableTime(v.ReadyAt)).Scan(&v.RowVersion, &v.UpdatedAt)
	if err != nil {
		return biz.ErrTenantEnvironmentPersistence
	}
	return nil
}

type advisoryLock struct {
	conn *pgxpool.Conn
	key  int64
}

func (l *advisoryLock) Release() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var released bool
	err := l.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, l.key).Scan(&released)
	if err != nil || !released {
		conn := l.conn.Hijack()
		_ = conn.Close(ctx)
	} else {
		l.conn.Release()
	}
	l.conn = nil
}
func (r *Repository) TryLock(ctx context.Context, tenant, cluster string) (biz.TenantEnvironmentLock, bool, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, biz.ErrTenantEnvironmentPersistence
	}
	key := biz.TenantEnvironmentLockKey(tenant, cluster)
	var held bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); err != nil {
		raw := conn.Hijack()
		clean, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = raw.Close(clean)
		return nil, false, biz.ErrTenantEnvironmentPersistence
	}
	if !held {
		conn.Release()
		return nil, false, nil
	}
	return &advisoryLock{conn: conn, key: key}, true, nil
}
func (r *Repository) ReadReadyBinding(ctx context.Context, tenant, cluster string) (projection.Binding, error) {
	v, err := r.Get(ctx, tenant, cluster)
	if err != nil {
		return projection.Binding{}, err
	}
	if v.State != biz.EnvironmentReady {
		reason := v.ReasonCode
		if reason == "" {
			reason = "ENVIRONMENT_PROVISIONING"
		}
		return projection.Binding{}, biz.EnvironmentError(reason)
	}
	if biz.EnvironmentDigest(v.BindingCanonical) != v.BindingDigest {
		return projection.Binding{}, biz.ErrTenantEnvironmentPersistence
	}
	b, err := projection.Decode(v.BindingCanonical)
	if err != nil || b.TenantID != tenant || b.Environment.ClusterID != cluster || b.Environment.BindingID != v.ID || b.Environment.NamespaceUID != v.NamespaceUID || b.Environment.ExperimentID != v.ExperimentID {
		return b, biz.ErrTenantEnvironmentPersistence
	}
	return b, nil
}
