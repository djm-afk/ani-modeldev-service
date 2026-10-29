package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// The database contract is the seam here: a restricted runtime caller must be
// able to store the current verified facts without an old size or row ceiling.
func TestCurrentBaselineAcceptsLargeInputAndExactInt64PreparedFacts(t *testing.T) {
	pool := postgres.Prepare(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tenant, input := uuid.NewString(), uuid.NewString()
	_, err := pool.Exec(ctx, `INSERT INTO modeldev_input_versions (
		tenant_id,input_version_id,request_id,actor,requested_at,
		storage_connection_id,bucket,approved_prefix,object_key,object_version_id,
		size_bytes,sha256,format
	) VALUES ($1,$2,$3,'current-runtime',clock_timestamp(),
		'current-store','test-source','datasets/','datasets/current.csv','fixed-version',
		17179869184,repeat('a',64),'CSV')`, tenant, input, uuid.NewString())
	if err != nil {
		t.Fatalf("current runtime could not reserve a 16 GiB source: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE modeldev_input_versions SET
		state='READY',verified_at=clock_timestamp(),verified_schema_version='ani.cpu.csv.v1',
		verified_row_count=9223372036854775807,verified_feature_count=16,
		prepared_format='CPU_CSV',prepared_transform_id='csv-identity-v3',
		prepared_size_bytes=17179869184,prepared_sha256=repeat('a',64),
		prepared_row_count=9223372036854775807
		WHERE tenant_id=$1 AND input_version_id=$2`, tenant, input)
	if err != nil {
		t.Fatalf("current runtime could not finish verified source/prepared facts: %v", err)
	}
	var sourceBytes, rows, preparedBytes, preparedRows int64
	if err := pool.QueryRow(ctx, `SELECT size_bytes,verified_row_count,prepared_size_bytes,prepared_row_count
		FROM modeldev_input_versions WHERE tenant_id=$1 AND input_version_id=$2`, tenant, input).
		Scan(&sourceBytes, &rows, &preparedBytes, &preparedRows); err != nil {
		t.Fatal(err)
	}
	if sourceBytes != 17179869184 || preparedBytes != 17179869184 || rows != 9223372036854775807 || preparedRows != 9223372036854775807 {
		t.Fatal("current bigint facts lost precision or acquired a historical content ceiling")
	}
	_, err = pool.Exec(ctx, `UPDATE modeldev_input_versions SET prepared_row_count=9223372036854775806
		WHERE tenant_id=$1 AND input_version_id=$2`, tenant, input)
	assertDatabaseCode(t, err, "23514", "different prepared/source row counts")
	_, err = pool.Exec(ctx, `UPDATE modeldev_input_versions SET prepared_size_bytes=17179869183
		WHERE tenant_id=$1 AND input_version_id=$2`, tenant, input)
	assertDatabaseCode(t, err, "23514", "CSV content changed by an identity transform")
}

func TestCurrentBaselineRestrictsRuntimeAndPreservesExactOwnerRevision(t *testing.T) {
	pool := postgres.Prepare(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tableCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema=current_schema() AND table_type='BASE TABLE'`).Scan(&tableCount); err != nil || tableCount != 9 {
		t.Fatalf("empty current baseline must initialize exactly nine application tables: count=%d err=%v", tableCount, err)
	}
	tenant, execution, _ := insertCurrentIdentity(t, ctx, pool)
	_, err := pool.Exec(ctx, `UPDATE modeldev_execution_identities SET owner_revision=18446744073709551615
		WHERE tenant_id=$1 AND execution_id=$2`, tenant, execution)
	if err != nil {
		t.Fatalf("current owner revision must retain the exact uint64 maximum: %v", err)
	}
	for _, invalid := range []string{"-1", "1.5", "18446744073709551616"} {
		_, err := pool.Exec(ctx, `UPDATE modeldev_execution_identities SET owner_revision=$3::numeric
			WHERE tenant_id=$1 AND execution_id=$2`, tenant, execution, invalid)
		assertDatabaseCode(t, err, "23514", "invalid current owner revision "+invalid)
	}
	var revision string
	if err := pool.QueryRow(ctx, `SELECT owner_revision::text FROM modeldev_execution_identities
		WHERE tenant_id=$1 AND execution_id=$2`, tenant, execution).Scan(&revision); err != nil || revision != "18446744073709551615" {
		t.Fatalf("rejected revisions must leave the exact committed owner revision intact: value=%s err=%v", revision, err)
	}
	for behavior, statement := range map[string]string{
		"runtime schema creation":    `CREATE TABLE runtime_must_not_migrate (id integer)`,
		"immutable identity rewrite": `UPDATE modeldev_execution_identities SET spec_hash=repeat('b',64)`,
		"runtime business deletion":  `DELETE FROM modeldev_execution_identities`,
	} {
		_, err := pool.Exec(ctx, statement)
		assertDatabaseCode(t, err, "42501", behavior)
	}
}

func TestCurrentBaselineRequiresTenantBoundAuthorityForComputeFacts(t *testing.T) {
	pool := postgres.Prepare(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tenant, execution, operation := insertCurrentIdentity(t, ctx, pool)
	_, err := pool.Exec(ctx, `INSERT INTO modeldev_execution_runtimes
		(tenant_id,execution_id,operation_id,spec_hash,facts)
		VALUES ($1,$2,$3,repeat('a',64),'{"CloseGeneration":0}')`, tenant, execution, operation)
	assertDatabaseCode(t, err, "23503", "an OPEN runtime without bound Run authority")
	// A no-dispatch close can persist without acquiring compute authority. All
	// compute facts are explicitly absent, matching the current runtime writer.
	_, err = pool.Exec(ctx, `INSERT INTO modeldev_execution_runtimes
		(tenant_id,execution_id,operation_id,spec_hash,facts,
		 close_generation,close_reason,close_requested_at)
		VALUES ($1,$2,$3,repeat('a',64),
		 '{"CloseGeneration":1,"Workspace":null,"Training":null,"TrainingHandle":null,"Observation":null,"Publication":null}',
		 1,'USER_STOP',clock_timestamp())`, tenant, execution, operation)
	if err != nil {
		t.Fatalf("a fenced no-dispatch close must remain persistable: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE modeldev_execution_runtimes
		SET facts=jsonb_set(facts,'{Workspace}','{}')
		WHERE tenant_id=$1 AND execution_id=$2`, tenant, execution)
	assertDatabaseCode(t, err, "23503", "workspace creation hidden behind an existing close fence")
	_, err = pool.Exec(ctx, `INSERT INTO modeldev_close_intents
		(tenant_id,execution_id,operation_id,spec_hash,source_kind,source_generation,
		 owner_generation,reason,requested_at,requested_actor,close_state)
		VALUES ($1,$2,$3,repeat('a',64),'GOVERNANCE',1,1,'USER_STOP',
		 clock_timestamp(),'current-runtime','CLOSING')`, uuid.NewString(), execution, operation)
	assertDatabaseCode(t, err, "23503", "a close intent borrowing another tenant's execution identity")
}

func assertDatabaseCode(t *testing.T, err error, want, behavior string) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != want {
		t.Fatalf("database must reject %s with SQLSTATE %s; got %v", behavior, want, err)
	}
}

func insertCurrentIdentity(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string, string) {
	t.Helper()
	tenant, execution, operation := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO modeldev_execution_identities
		(tenant_id,execution_id,operation_id,spec_hash)
		VALUES ($1,$2,$3,repeat('a',64))`, tenant, execution, operation); err != nil {
		t.Fatal(err)
	}
	return tenant, execution, operation
}
