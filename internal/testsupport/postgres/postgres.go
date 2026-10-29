// Package postgres contains only the shared CPU-P01 real-database test fixture.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This preflight and current schema setup are deliberately distinct from the product
// assertion. Any failure here is a missing/broken real dependency, not a valid
// TDD RED. No connection URL or raw connection error is included in test output.
func Prepare(t *testing.T) func() *pgxpool.Pool {
	t.Helper()
	fixture := prepareSchema(t)
	fixture.install(t)
	t.Log("CPU04_DB_PREFLIGHT PASS: real PostgreSQL, restricted runtime role, isolated current schema baseline")
	return func() *pgxpool.Pool { return fixture.openRuntimePool(t) }
}

// schemaFixture owns one random schema and both restricted connection roles.
// The sole current baseline fixes all tables and runtime grants.
type schemaFixture struct {
	adminPool     *pgxpool.Pool
	runtimeConfig *pgxpool.Config
	schemaName    string
	schemaSQL     string
	roleSQL       string
}

func prepareSchema(t *testing.T) *schemaFixture {
	t.Helper()
	runtimeURL := os.Getenv("CPU_P01_TEST_DATABASE_URL")
	adminURL := os.Getenv("CPU_P01_TEST_DATABASE_ADMIN_URL")
	if runtimeURL == "" || adminURL == "" {
		t.Fatal("CPU04_DB_PREFLIGHT: both protected test database references are required; behavior NOT_RUN")
	}
	runtimeConfig, err := pgxpool.ParseConfig(runtimeURL)
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime connection configuration invalid; behavior NOT_RUN")
	}
	adminConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration connection configuration invalid; behavior NOT_RUN")
	}
	if runtimeConfig.ConnConfig.Host != adminConfig.ConnConfig.Host || runtimeConfig.ConnConfig.Port != adminConfig.ConnConfig.Port || runtimeConfig.ConnConfig.Database != adminConfig.ConnConfig.Database || runtimeConfig.ConnConfig.User == adminConfig.ConnConfig.User {
		t.Fatal("CPU04_DB_PREFLIGHT: separate runtime/migration roles on the same test database are required; behavior NOT_RUN")
	}
	runtimeConfig.MaxConns = 2
	adminConfig.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration pool creation failed; behavior NOT_RUN")
	}
	t.Cleanup(adminPool.Close)
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration database connection failed; behavior NOT_RUN")
	}
	preflightPool, err := pgxpool.NewWithConfig(ctx, runtimeConfig.Copy())
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime pool creation failed; behavior NOT_RUN")
	}
	defer preflightPool.Close()
	var superuser, bypassRLS bool
	if err := preflightPool.QueryRow(ctx, "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&superuser, &bypassRLS); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime role inspection failed; behavior NOT_RUN")
	}
	if superuser || bypassRLS {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime role must be NOSUPERUSER NOBYPASSRLS; behavior NOT_RUN")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: schema identifier allocation failed; behavior NOT_RUN")
	}
	schemaName := "cpu04_" + hex.EncodeToString(random[:])
	schemaSQL := pgx.Identifier{schemaName}.Sanitize()
	roleSQL := pgx.Identifier{runtimeConfig.ConnConfig.User}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: isolated schema creation failed; behavior NOT_RUN")
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupContext, "DROP SCHEMA "+schemaSQL+" CASCADE"); err != nil {
			t.Errorf("CPU04_DB_CLEANUP: isolated schema cleanup failed")
		}
	})
	runtimeConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	return &schemaFixture{adminPool: adminPool, runtimeConfig: runtimeConfig, schemaName: schemaName, schemaSQL: schemaSQL, roleSQL: roleSQL}
}

func (fixture *schemaFixture) migrationTransaction(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	transaction, err := fixture.adminPool.Begin(ctx)
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration transaction failed; behavior NOT_RUN")
	}
	t.Cleanup(func() { rollbackMigration(transaction) })
	if _, err := transaction.Exec(ctx, "SET LOCAL search_path TO "+fixture.schemaSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration schema binding failed; behavior NOT_RUN")
	}
	return transaction
}

func (fixture *schemaFixture) install(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transaction := fixture.migrationTransaction(t, ctx)
	defer rollbackMigration(transaction)
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "migrations", "0001_current_baseline.up.sql"))
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: current baseline unavailable; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, string(raw)); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: current baseline initialization failed; behavior NOT_RUN")
	}
	schemaSQL, roleSQL := fixture.schemaSQL, fixture.roleSQL
	if _, err := transaction.Exec(ctx, "GRANT USAGE ON SCHEMA "+schemaSQL+" TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime schema grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_executions, "+schemaSQL+".modeldev_execution_identities, "+schemaSQL+".modeldev_close_intents, "+schemaSQL+".modeldev_input_versions TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime table grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE (close_generation, owner_revision) ON "+schemaSQL+".modeldev_execution_identities TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime owner-generation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_pipeline_dispatches TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime dispatch-reservation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_pipeline_confirmed_runs TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime confirmed-run observation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_run_authorities TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime Run authority grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT, UPDATE ON "+schemaSQL+".modeldev_execution_cleanup_audits, "+schemaSQL+".modeldev_execution_runtimes TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime lifecycle grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE (state, uncertain_at, not_sent_at) ON "+schemaSQL+".modeldev_pipeline_dispatches TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime submission-observation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE (state, verified_at, verified_schema_version, verified_row_count, verified_feature_count, prepared_format, prepared_transform_id, prepared_size_bytes, prepared_sha256, prepared_row_count, failure_code, failure_observed_at) ON "+schemaSQL+".modeldev_input_versions TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime input-verification grant failed; behavior NOT_RUN")
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: migration commit failed; behavior NOT_RUN")
	}
}

func (fixture *schemaFixture) openRuntimePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, fixture.runtimeConfig.Copy())
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: isolated runtime pool creation failed; behavior NOT_RUN")
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: isolated runtime connection failed; behavior NOT_RUN")
	}
	return pool
}

func rollbackMigration(transaction pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = transaction.Rollback(ctx)
}

// Tests in cmd and deeper adapter packages share the same versioned schema.
// Locate the checked-out module instead of depending on the package depth.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: cannot locate checkout; behavior NOT_RUN")
	}
	for {
		if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && info.Mode().IsRegular() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("CPU04_DB_PREFLIGHT: module root missing; behavior NOT_RUN")
		}
		directory = parent
	}
}
