// Package postgres contains only the shared CPU-P01 real-database test fixture.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This preflight and migration setup are deliberately distinct from the product
// assertion. Any failure here is a missing/broken real dependency, not a valid
// TDD RED. No connection URL or raw connection error is included in test output.
func Prepare(t *testing.T) func() *pgxpool.Pool {
	t.Helper()
	fixture := prepareSchema(t)
	migrations, err := filepath.Glob(filepath.Join(repositoryRoot(t), "migrations", "*.up.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatal("CPU04_DB_PREFLIGHT: versioned execution migrations missing; behavior NOT_RUN")
	}
	sort.Strings(migrations)
	fixture.install(t, migrations, true)
	t.Log("CPU04_DB_PREFLIGHT PASS: real PostgreSQL, restricted runtime role, isolated versioned schema")
	return func() *pgxpool.Pool { return fixture.openRuntimePool(t) }
}

// schemaFixture owns one random schema and both restricted connection roles.
// Only the ordinary full-schema fixture and the fixed revision-upgrade fixture
// choose its migration files; callers cannot select tables or runtime grants.
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

// applyMigrationFiles mirrors the documented production procedure, which runs
// each file as its own transaction. Sharing one transaction across the set is
// not equivalent: a file that carries its own BEGIN/COMMIT (0019 does) commits
// the surrounding transaction, and every later file would then run against a
// closed one.
func (fixture *schemaFixture) applyMigrationFiles(t *testing.T, ctx context.Context, migrations []string) {
	t.Helper()
	for _, migrationPath := range migrations {
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("CPU04_DB_PREFLIGHT: cannot read migration %s; behavior NOT_RUN", filepath.Base(migrationPath))
		}
		transaction := fixture.migrationTransaction(t, ctx)
		if _, err := transaction.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("CPU04_DB_PREFLIGHT: migration %s failed; behavior NOT_RUN", filepath.Base(migrationPath))
		}
		// A file with its own COMMIT has already closed this transaction, and
		// pgx reports a redundant COMMIT as a warning rather than an error.
		if err := transaction.Commit(ctx); err != nil {
			t.Fatalf("CPU04_DB_PREFLIGHT: migration %s did not commit; behavior NOT_RUN", filepath.Base(migrationPath))
		}
	}
}

func (fixture *schemaFixture) install(t *testing.T, migrations []string, ownerRevision bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture.applyMigrationFiles(t, ctx, migrations)
	transaction := fixture.migrationTransaction(t, ctx)
	defer rollbackMigration(transaction)
	schemaSQL, roleSQL := fixture.schemaSQL, fixture.roleSQL
	if _, err := transaction.Exec(ctx, "GRANT USAGE ON SCHEMA "+schemaSQL+" TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime schema grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_executions, "+schemaSQL+".modeldev_execution_identities, "+schemaSQL+".modeldev_close_intents, "+schemaSQL+".modeldev_input_versions TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime table grant failed; behavior NOT_RUN")
	}
	ownerColumns := "close_generation"
	if ownerRevision {
		ownerColumns += ", owner_revision"
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE ("+ownerColumns+") ON "+schemaSQL+".modeldev_execution_identities TO "+roleSQL); err != nil {
		t.Fatal("CPU04_DB_PREFLIGHT: runtime owner-generation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_pipeline_dispatches TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime dispatch-reservation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_pipeline_confirmed_runs TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime confirmed-run observation grant failed; behavior NOT_RUN")
	}
	// Historical revision-upgrade fixtures install only their fixed migration
	// set. Grant the new immutable table only when that migration was installed.
	for _, migration := range migrations {
		if filepath.Base(migration) == "0013_run_authority.up.sql" {
			if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT ON "+schemaSQL+".modeldev_run_authorities TO "+roleSQL); err != nil {
				t.Fatal("CPU07_DB_PREFLIGHT: runtime Run authority grant failed; behavior NOT_RUN")
			}
			break
		}
	}
	for _, migration := range migrations {
		if filepath.Base(migration) == "0016_execution_cleanup_audit.up.sql" {
			if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT, UPDATE ON "+schemaSQL+".modeldev_execution_cleanup_audits TO "+roleSQL); err != nil {
				t.Fatal("CPU10_DB_PREFLIGHT: runtime cleanup audit grant failed; behavior NOT_RUN")
			}
			break
		}
	}
	for _, migration := range migrations {
		if filepath.Base(migration) == "0014_execution_runtime.up.sql" {
			if _, err := transaction.Exec(ctx, "GRANT INSERT, SELECT, UPDATE ON "+schemaSQL+".modeldev_execution_runtimes TO "+roleSQL); err != nil {
				t.Fatal("CPU04_DB_PREFLIGHT: runtime lifecycle grant failed; behavior NOT_RUN")
			}
			break
		}
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE (state, uncertain_at, not_sent_at) ON "+schemaSQL+".modeldev_pipeline_dispatches TO "+roleSQL); err != nil {
		t.Fatal("CPU07_DB_PREFLIGHT: runtime submission-observation grant failed; behavior NOT_RUN")
	}
	if _, err := transaction.Exec(ctx, "GRANT UPDATE (state, verified_at, verified_schema_version, verified_row_count, verified_feature_count, failure_code, failure_observed_at) ON "+schemaSQL+".modeldev_input_versions TO "+roleSQL); err != nil {
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
