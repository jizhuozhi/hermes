package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/model"

	"github.com/lib/pq"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

// PgStore implements Store backed by PostgreSQL.
type PgStore struct {
	db         *sql.DB
	logger     *zap.SugaredLogger
	maxHistory int
}

func NewPgStore(dsn string, logger *zap.SugaredLogger) (*PgStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pg open: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pg ping: %w", err)
	}

	s := &PgStore{db: db, logger: logger, maxHistory: 50}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("pg migrate: %w", err)
	}
	return s, nil
}

func (s *PgStore) Close() {
	s.db.Close()
}

// Schema migration
func (s *PgStore) migrate(ctx context.Context) error {
	ddl := `
-- ── Namespaces ───────────────────────────────────
CREATE TABLE IF NOT EXISTS namespaces (
    name       TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO namespaces (name) VALUES ('default') ON CONFLICT DO NOTHING;

-- ── Configuration ────────────────────────────────
CREATE TABLE IF NOT EXISTS domains (
    namespace  TEXT NOT NULL DEFAULT 'default',
    name       TEXT NOT NULL,
    config     JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (namespace, name)
);

CREATE TABLE IF NOT EXISTS clusters (
    namespace  TEXT NOT NULL DEFAULT 'default',
    name       TEXT NOT NULL,
    config     JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (namespace, name)
);

-- ── Change tracking ──────────────────────────────
CREATE TABLE IF NOT EXISTS config_history (
    id         BIGSERIAL PRIMARY KEY,
    namespace  TEXT NOT NULL DEFAULT 'default',
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    version    BIGINT NOT NULL,
    action     TEXT NOT NULL,
    operator   TEXT NOT NULL DEFAULT '',
    config     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_history_ns_kind_name ON config_history(namespace, kind, name, version DESC);

CREATE TABLE IF NOT EXISTS change_log (
    revision   BIGSERIAL PRIMARY KEY,
    namespace  TEXT NOT NULL DEFAULT 'default',
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    action     TEXT NOT NULL,
    operator   TEXT NOT NULL DEFAULT '',
    config     JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_changelog_ns_revision ON change_log(namespace, revision);

-- ── Runtime status ───────────────────────────────
CREATE TABLE IF NOT EXISTS gateway_instances (
    namespace         TEXT NOT NULL DEFAULT 'default',
    id                TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT '',
    started_at        TEXT NOT NULL DEFAULT '',
    registered_at     TEXT NOT NULL DEFAULT '',
    last_keepalive_at TEXT NOT NULL DEFAULT '',
    config_revision   BIGINT NOT NULL DEFAULT 0,
    last_seen_at      TEXT NOT NULL DEFAULT '',
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (namespace, id)
) WITH (fillfactor = 70);

CREATE TABLE IF NOT EXISTS controller_status (
    namespace         TEXT NOT NULL DEFAULT 'default',
    id                TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT '',
    is_leader         BOOLEAN NOT NULL DEFAULT FALSE,
    started_at        TEXT NOT NULL DEFAULT '',
    last_heartbeat_at TEXT NOT NULL DEFAULT '',
    config_revision   BIGINT NOT NULL DEFAULT 0,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (namespace, id)
) WITH (fillfactor = 70);

-- ── Credentials (HMAC) ──────────────────────────
CREATE TABLE IF NOT EXISTS api_credentials (
    id          BIGSERIAL PRIMARY KEY,
    namespace   TEXT NOT NULL DEFAULT 'default',
    access_key  TEXT NOT NULL UNIQUE,
    secret_key  TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    scopes      TEXT[] NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── RBAC ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    sub        TEXT PRIMARY KEY,
    username   TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL DEFAULT '',
    is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
    password_hash TEXT NOT NULL DEFAULT '',
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Migration: add password_hash if not exists (idempotent).
DO $$ BEGIN
    ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
EXCEPTION WHEN others THEN NULL;
END $$;
-- Migration: add must_change_password flag (idempotent).
DO $$ BEGIN
    ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE;
EXCEPTION WHEN others THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS namespace_members (
    namespace  TEXT NOT NULL,
    user_sub   TEXT NOT NULL REFERENCES users(sub) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'viewer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (namespace, user_sub)
);
CREATE INDEX IF NOT EXISTS idx_ns_members_user ON namespace_members(user_sub);

CREATE TABLE IF NOT EXISTS group_bindings (
    id         BIGSERIAL PRIMARY KEY,
    namespace  TEXT NOT NULL,
    group_name TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'viewer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(namespace, group_name)
);
CREATE INDEX IF NOT EXISTS idx_group_bindings_ns ON group_bindings(namespace);

-- ── Misc ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS grafana_dashboards (
    id        BIGSERIAL PRIMARY KEY,
    namespace TEXT NOT NULL DEFAULT 'default',
    name      TEXT NOT NULL,
    url       TEXT NOT NULL
);

-- ── JWT Signing Keys (builtin auth) ─────────────
CREATE TABLE IF NOT EXISTS jwt_signing_keys (
    kid        TEXT PRIMARY KEY,
    secret     BYTEA NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active',   -- 'active' or 'retired'
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ                        -- NULL for active, set when retired
);
CREATE INDEX IF NOT EXISTS idx_jwt_keys_status ON jwt_signing_keys(status);
`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("pg migrate: %w", err)
	}
	return nil
}

// Domain CRUD
func (s *PgStore) ListDomains(ctx context.Context, ns string) ([]model.DomainConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT config FROM domains WHERE namespace = $1 ORDER BY name`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list domains: %w", err)
	}
	defer rows.Close()

	var domains []model.DomainConfig
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("pg scan domain: %w", err)
		}
		var d model.DomainConfig
		if err := json.Unmarshal(data, &d); err != nil {
			s.logger.Warnf("skipping corrupt domain: %v", err)
			continue
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

func (s *PgStore) GetDomain(ctx context.Context, ns, name string) (*model.DomainConfig, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT config FROM domains WHERE namespace = $1 AND name = $2`, ns, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get domain: %w", err)
	}
	var d model.DomainConfig
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("unmarshal domain: %w", err)
	}
	return &d, nil
}

func (s *PgStore) PutDomain(ctx context.Context, ns string, domain *model.DomainConfig, action, operator string) (int64, error) {
	data, err := json.Marshal(domain)
	if err != nil {
		return 0, fmt.Errorf("marshal domain: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO domains (namespace, name, config, updated_at) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (namespace, name) DO UPDATE SET config = $3, updated_at = NOW()`,
		ns, domain.Name, data)
	if err != nil {
		return 0, fmt.Errorf("pg upsert domain: %w", err)
	}

	version, err := s.nextVersion(ctx, tx, ns, "domain", domain.Name)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'domain', $2, $3, $4, $5, $6)`,
		ns, domain.Name, version, action, operator, data)
	if err != nil {
		return 0, fmt.Errorf("pg insert domain history: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'domain', $2, $3, $4, $5)`,
		ns, domain.Name, action, operator, data)
	if err != nil {
		return 0, fmt.Errorf("pg insert change_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pg commit: %w", err)
	}

	go s.pruneHistory(context.Background(), ns, "domain", domain.Name)

	s.logger.Infof("domain written: ns=%s name=%s, action=%s, operator=%s, version=%d", ns, domain.Name, action, operator, version)
	return version, nil
}

func (s *PgStore) DeleteDomain(ctx context.Context, ns, name, operator string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	// Read current value inside the transaction to avoid TOCTOU.
	var configData []byte
	err = tx.QueryRowContext(ctx, `SELECT config FROM domains WHERE namespace = $1 AND name = $2`, ns, name).Scan(&configData)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("domain %q not found", name)
	}
	if err != nil {
		return 0, fmt.Errorf("pg get domain for delete: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM domains WHERE namespace = $1 AND name = $2`, ns, name)
	if err != nil {
		return 0, fmt.Errorf("pg delete domain: %w", err)
	}

	version, err := s.nextVersion(ctx, tx, ns, "domain", name)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'domain', $2, $3, 'delete', $4, $5)`,
		ns, name, version, operator, configData)
	if err != nil {
		return 0, fmt.Errorf("pg insert domain delete history: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'domain', $2, 'delete', $3, NULL)`,
		ns, name, operator)
	if err != nil {
		return 0, fmt.Errorf("pg insert change_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pg commit: %w", err)
	}

	s.logger.Infof("domain deleted: ns=%s name=%s, operator=%s, version=%d", ns, name, operator, version)
	return version, nil
}

// Cluster CRUD
func (s *PgStore) ListClusters(ctx context.Context, ns string) ([]model.ClusterConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT config FROM clusters WHERE namespace = $1 ORDER BY name`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list clusters: %w", err)
	}
	defer rows.Close()

	var clusters []model.ClusterConfig
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("pg scan cluster: %w", err)
		}
		var c model.ClusterConfig
		if err := json.Unmarshal(data, &c); err != nil {
			s.logger.Warnf("skipping corrupt cluster: %v", err)
			continue
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

func (s *PgStore) GetCluster(ctx context.Context, ns, name string) (*model.ClusterConfig, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT config FROM clusters WHERE namespace = $1 AND name = $2`, ns, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get cluster: %w", err)
	}
	var c model.ClusterConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("unmarshal cluster: %w", err)
	}
	return &c, nil
}

func (s *PgStore) PutCluster(ctx context.Context, ns string, cluster *model.ClusterConfig, action, operator string) (int64, error) {
	data, err := json.Marshal(cluster)
	if err != nil {
		return 0, fmt.Errorf("marshal cluster: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO clusters (namespace, name, config, updated_at) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (namespace, name) DO UPDATE SET config = $3, updated_at = NOW()`,
		ns, cluster.Name, data)
	if err != nil {
		return 0, fmt.Errorf("pg upsert cluster: %w", err)
	}

	version, err := s.nextVersion(ctx, tx, ns, "cluster", cluster.Name)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'cluster', $2, $3, $4, $5, $6)`,
		ns, cluster.Name, version, action, operator, data)
	if err != nil {
		return 0, fmt.Errorf("pg insert cluster history: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'cluster', $2, $3, $4, $5)`,
		ns, cluster.Name, action, operator, data)
	if err != nil {
		return 0, fmt.Errorf("pg insert change_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pg commit: %w", err)
	}

	go s.pruneHistory(context.Background(), ns, "cluster", cluster.Name)

	s.logger.Infof("cluster written: ns=%s name=%s, action=%s, operator=%s, version=%d", ns, cluster.Name, action, operator, version)
	return version, nil
}

func (s *PgStore) DeleteCluster(ctx context.Context, ns, name, operator string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	// Read current value inside the transaction to avoid TOCTOU.
	var configData []byte
	err = tx.QueryRowContext(ctx, `SELECT config FROM clusters WHERE namespace = $1 AND name = $2`, ns, name).Scan(&configData)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("cluster %q not found", name)
	}
	if err != nil {
		return 0, fmt.Errorf("pg get cluster for delete: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM clusters WHERE namespace = $1 AND name = $2`, ns, name)
	if err != nil {
		return 0, fmt.Errorf("pg delete cluster: %w", err)
	}

	version, err := s.nextVersion(ctx, tx, ns, "cluster", name)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'cluster', $2, $3, 'delete', $4, $5)`,
		ns, name, version, operator, configData)
	if err != nil {
		return 0, fmt.Errorf("pg insert cluster delete history: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'cluster', $2, 'delete', $3, NULL)`,
		ns, name, operator)
	if err != nil {
		return 0, fmt.Errorf("pg insert change_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pg commit: %w", err)
	}

	s.logger.Infof("cluster deleted: ns=%s name=%s, operator=%s, version=%d", ns, name, operator, version)
	return version, nil
}

// Bulk operations
func (s *PgStore) PutAllConfig(ctx context.Context, ns string, domains []model.DomainConfig, clusters []model.ClusterConfig, operator string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	// Clear existing within namespace
	if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE namespace = $1`, ns); err != nil {
		return 0, fmt.Errorf("pg truncate domains: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM clusters WHERE namespace = $1`, ns); err != nil {
		return 0, fmt.Errorf("pg truncate clusters: %w", err)
	}

	// Insert clusters
	for i := range clusters {
		data, err := json.Marshal(&clusters[i])
		if err != nil {
			return 0, fmt.Errorf("marshal cluster %s: %w", clusters[i].Name, err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO clusters (namespace, name, config) VALUES ($1, $2, $3)`,
			ns, clusters[i].Name, data)
		if err != nil {
			return 0, fmt.Errorf("pg insert cluster %s: %w", clusters[i].Name, err)
		}
		ver, err := s.nextVersionTx(ctx, tx, ns, "cluster", clusters[i].Name)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'cluster', $2, $3, 'import', $4, $5)`,
			ns, clusters[i].Name, ver, operator, data); err != nil {
			return 0, fmt.Errorf("pg insert cluster history (import): %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'cluster', $2, 'import', $3, $4)`,
			ns, clusters[i].Name, operator, data); err != nil {
			return 0, fmt.Errorf("pg insert cluster change_log (import): %w", err)
		}
	}

	// Insert domains
	for i := range domains {
		data, err := json.Marshal(&domains[i])
		if err != nil {
			return 0, fmt.Errorf("marshal domain %s: %w", domains[i].Name, err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO domains (namespace, name, config) VALUES ($1, $2, $3)`,
			ns, domains[i].Name, data)
		if err != nil {
			return 0, fmt.Errorf("pg insert domain %s: %w", domains[i].Name, err)
		}
		ver, err := s.nextVersionTx(ctx, tx, ns, "domain", domains[i].Name)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO config_history (namespace, kind, name, version, action, operator, config) VALUES ($1, 'domain', $2, $3, 'import', $4, $5)`,
			ns, domains[i].Name, ver, operator, data); err != nil {
			return 0, fmt.Errorf("pg insert domain history (import): %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO change_log (namespace, kind, name, action, operator, config) VALUES ($1, 'domain', $2, 'import', $3, $4)`,
			ns, domains[i].Name, operator, data); err != nil {
			return 0, fmt.Errorf("pg insert domain change_log (import): %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pg commit: %w", err)
	}

	s.logger.Infof("all config replaced: ns=%s, domains=%d, clusters=%d", ns, len(domains), len(clusters))
	return 0, nil
}

func (s *PgStore) GetConfig(ctx context.Context, ns string) (*model.GatewayConfig, error) {
	domains, err := s.ListDomains(ctx, ns)
	if err != nil {
		return nil, err
	}
	clusters, err := s.ListClusters(ctx, ns)
	if err != nil {
		return nil, err
	}
	if domains == nil {
		domains = []model.DomainConfig{}
	}
	if clusters == nil {
		clusters = []model.ClusterConfig{}
	}
	return &model.GatewayConfig{Domains: domains, Clusters: clusters}, nil
}

// Per-domain History
func (s *PgStore) GetDomainHistory(ctx context.Context, ns, name string) ([]HistoryEntry, error) {
	return s.getHistory(ctx, ns, "domain", name)
}

func (s *PgStore) GetDomainVersion(ctx context.Context, ns, name string, version int64) (*HistoryEntry, error) {
	return s.getVersion(ctx, ns, "domain", name, version)
}

func (s *PgStore) RollbackDomain(ctx context.Context, ns, name string, version int64, operator string) (int64, error) {
	entry, err := s.GetDomainVersion(ctx, ns, name, version)
	if err != nil {
		return 0, err
	}
	if entry == nil {
		return 0, fmt.Errorf("domain %q version %d not found", name, version)
	}
	if entry.Domain == nil {
		return 0, fmt.Errorf("domain %q version %d is a delete entry, cannot rollback", name, version)
	}
	return s.PutDomain(ctx, ns, entry.Domain, "rollback", operator)
}

// Per-cluster History
func (s *PgStore) GetClusterHistory(ctx context.Context, ns, name string) ([]HistoryEntry, error) {
	return s.getHistory(ctx, ns, "cluster", name)
}

func (s *PgStore) GetClusterVersion(ctx context.Context, ns, name string, version int64) (*HistoryEntry, error) {
	return s.getVersion(ctx, ns, "cluster", name, version)
}

func (s *PgStore) RollbackCluster(ctx context.Context, ns, name string, version int64, operator string) (int64, error) {
	entry, err := s.GetClusterVersion(ctx, ns, name, version)
	if err != nil {
		return 0, err
	}
	if entry == nil {
		return 0, fmt.Errorf("cluster %q version %d not found", name, version)
	}
	if entry.Cluster == nil {
		return 0, fmt.Errorf("cluster %q version %d is a delete entry, cannot rollback", name, version)
	}
	return s.PutCluster(ctx, ns, entry.Cluster, "rollback", operator)
}

// Watch (long-poll for controller)
func (s *PgStore) CurrentRevision(ctx context.Context, ns string) (int64, error) {
	var rev sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(revision) FROM change_log WHERE namespace = $1`, ns).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("pg current revision: %w", err)
	}
	if !rev.Valid {
		return 0, nil
	}
	return rev.Int64, nil
}

func (s *PgStore) WatchFrom(ctx context.Context, ns string, sinceRevision int64) ([]ChangeEvent, int64, error) {
	// Simple short-poll: query once and return immediately.
	return s.queryChanges(ctx, ns, sinceRevision)
}

func (s *PgStore) queryChanges(ctx context.Context, ns string, sinceRevision int64) ([]ChangeEvent, int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT revision, kind, name, action, config FROM change_log WHERE namespace = $1 AND revision > $2 ORDER BY revision LIMIT 100`,
		ns, sinceRevision)
	if err != nil {
		return nil, 0, fmt.Errorf("pg query changes: %w", err)
	}
	defer rows.Close()

	var events []ChangeEvent
	var maxRev int64
	for rows.Next() {
		var e ChangeEvent
		var data []byte
		if err := rows.Scan(&e.Revision, &e.Kind, &e.Name, &e.Action, &data); err != nil {
			return nil, 0, fmt.Errorf("pg scan change: %w", err)
		}
		if data != nil {
			switch e.Kind {
			case "domain":
				var d model.DomainConfig
				if json.Unmarshal(data, &d) == nil {
					e.Domain = &d
				}
			case "cluster":
				var c model.ClusterConfig
				if json.Unmarshal(data, &c) == nil {
					e.Cluster = &c
				}
			}
		}
		if e.Revision > maxRev {
			maxRev = e.Revision
		}
		events = append(events, e)
	}
	return events, maxRev, rows.Err()
}

// Namespaces
// ListNamespaces returns all registered namespaces.
func (s *PgStore) ListNamespaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM namespaces ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("pg list namespaces: %w", err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return nil, fmt.Errorf("pg scan namespace: %w", err)
		}
		result = append(result, ns)
	}
	return result, rows.Err()
}

// CreateNamespace inserts a new namespace. Returns error if it already exists.
func (s *PgStore) CreateNamespace(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO namespaces (name) VALUES ($1)`, name)
	if err != nil {
		return fmt.Errorf("pg create namespace: %w", err)
	}
	return nil
}

// Shared helpers
func (s *PgStore) nextVersion(ctx context.Context, tx *sql.Tx, ns, kind, name string) (int64, error) {
	return s.nextVersionTx(ctx, tx, ns, kind, name)
}

func (s *PgStore) nextVersionTx(ctx context.Context, tx *sql.Tx, ns, kind, name string) (int64, error) {
	var maxVer sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(version) FROM config_history WHERE namespace = $1 AND kind = $2 AND name = $3`,
		ns, kind, name).Scan(&maxVer)
	if err != nil {
		return 0, fmt.Errorf("pg next version: %w", err)
	}
	if !maxVer.Valid {
		return 1, nil
	}
	return maxVer.Int64 + 1, nil
}

func (s *PgStore) getHistory(ctx context.Context, ns, kind, name string) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version, created_at, kind, name, action, operator, config FROM config_history
		 WHERE namespace = $1 AND kind = $2 AND name = $3 ORDER BY version DESC LIMIT $4`,
		ns, kind, name, s.maxHistory)
	if err != nil {
		return nil, fmt.Errorf("pg get history: %w", err)
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var data []byte
		if err := rows.Scan(&e.Version, &e.Timestamp, &e.Kind, &e.Name, &e.Action, &e.Operator, &data); err != nil {
			return nil, fmt.Errorf("pg scan history: %w", err)
		}
		if data != nil {
			switch kind {
			case "domain":
				var d model.DomainConfig
				if json.Unmarshal(data, &d) == nil {
					e.Domain = &d
				}
			case "cluster":
				var c model.ClusterConfig
				if json.Unmarshal(data, &c) == nil {
					e.Cluster = &c
				}
			}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PgStore) getVersion(ctx context.Context, ns, kind, name string, version int64) (*HistoryEntry, error) {
	var e HistoryEntry
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT version, created_at, kind, name, action, operator, config FROM config_history
		 WHERE namespace = $1 AND kind = $2 AND name = $3 AND version = $4`,
		ns, kind, name, version).Scan(&e.Version, &e.Timestamp, &e.Kind, &e.Name, &e.Action, &e.Operator, &data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get version: %w", err)
	}
	if data != nil {
		switch kind {
		case "domain":
			var d model.DomainConfig
			if json.Unmarshal(data, &d) == nil {
				e.Domain = &d
			}
		case "cluster":
			var c model.ClusterConfig
			if json.Unmarshal(data, &c) == nil {
				e.Cluster = &c
			}
		}
	}
	return &e, nil
}

// Audit log (global change event stream)
func (s *PgStore) ListAuditLog(ctx context.Context, ns string, limit, offset int) ([]AuditEntry, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_log WHERE namespace = $1`, ns).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("pg count audit: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT revision, kind, name, action, operator, created_at FROM change_log WHERE namespace = $1 ORDER BY revision DESC LIMIT $2 OFFSET $3`,
		ns, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("pg list audit: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.Revision, &e.Kind, &e.Name, &e.Action, &e.Operator, &e.Timestamp); err != nil {
			return nil, 0, fmt.Errorf("pg scan audit: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func (s *PgStore) InsertAuditLog(ctx context.Context, ns, kind, name, action, operator string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO change_log (namespace, kind, name, action, operator) VALUES ($1, $2, $3, $4, $5)`,
		ns, kind, name, action, operator)
	if err != nil {
		return fmt.Errorf("pg insert audit log: %w", err)
	}
	return nil
}

func (s *PgStore) pruneHistory(ctx context.Context, ns, kind, name string) {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM config_history WHERE id IN (
			SELECT id FROM config_history
			WHERE namespace = $1 AND kind = $2 AND name = $3
			ORDER BY version DESC
			OFFSET $4
		)`, ns, kind, name, s.maxHistory)
	if err != nil {
		s.logger.Warnf("prune history (%s/%s/%s): %v", ns, kind, name, err)
	}
}

// Status (namespace-scoped)
func (s *PgStore) UpsertGatewayInstances(ctx context.Context, ns string, instances []GatewayInstanceStatus) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(instances) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_instances WHERE namespace = $1`, ns); err != nil {
			return fmt.Errorf("pg clear instances: %w", err)
		}
	} else {
		ids := make([]any, len(instances)+1)
		ids[0] = ns
		placeholders := ""
		for i, inst := range instances {
			ids[i+1] = inst.ID
			if i > 0 {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", i+2)
		}
		q := fmt.Sprintf(`DELETE FROM gateway_instances WHERE namespace = $1 AND id NOT IN (%s)`, placeholders)
		if _, err := tx.ExecContext(ctx, q, ids...); err != nil {
			return fmt.Errorf("pg prune stale instances: %w", err)
		}
	}

	for _, inst := range instances {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gateway_instances (namespace, id, status, started_at, registered_at, last_keepalive_at, config_revision, last_seen_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			ON CONFLICT (namespace, id) DO UPDATE SET
				status = EXCLUDED.status,
				started_at = EXCLUDED.started_at,
				registered_at = EXCLUDED.registered_at,
				last_keepalive_at = EXCLUDED.last_keepalive_at,
				config_revision = EXCLUDED.config_revision,
				last_seen_at = EXCLUDED.last_seen_at,
				updated_at = NOW()`,
			ns, inst.ID, inst.Status, inst.StartedAt, inst.RegisteredAt,
			inst.LastKeepaliveAt, inst.ConfigRevision, inst.LastSeenAt)
		if err != nil {
			return fmt.Errorf("pg upsert instance %s: %w", inst.ID, err)
		}
	}

	return tx.Commit()
}

func (s *PgStore) ListGatewayInstances(ctx context.Context, ns string) ([]GatewayInstanceStatus, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, status, started_at, registered_at, last_keepalive_at, config_revision, last_seen_at, updated_at
		 FROM gateway_instances WHERE namespace = $1 ORDER BY id`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list instances: %w", err)
	}
	defer rows.Close()

	var result []GatewayInstanceStatus
	for rows.Next() {
		var inst GatewayInstanceStatus
		if err := rows.Scan(&inst.ID, &inst.Status, &inst.StartedAt, &inst.RegisteredAt,
			&inst.LastKeepaliveAt, &inst.ConfigRevision, &inst.LastSeenAt, &inst.UpdatedAt); err != nil {
			return nil, fmt.Errorf("pg scan instance: %w", err)
		}
		result = append(result, inst)
	}
	return result, rows.Err()
}

func (s *PgStore) UpsertControllerStatus(ctx context.Context, ns string, ctrl *ControllerStatus) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO controller_status (namespace, id, status, is_leader, started_at, last_heartbeat_at, config_revision, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (namespace, id) DO UPDATE SET
			status = EXCLUDED.status,
			is_leader = EXCLUDED.is_leader,
			started_at = EXCLUDED.started_at,
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			config_revision = EXCLUDED.config_revision,
			updated_at = NOW()`,
		ns, ctrl.ID, ctrl.Status, ctrl.IsLeader, ctrl.StartedAt, ctrl.LastHeartbeatAt, ctrl.ConfigRevision)
	if err != nil {
		return fmt.Errorf("pg upsert controller: %w", err)
	}
	return nil
}

func (s *PgStore) GetControllerStatus(ctx context.Context, ns string) (*ControllerStatus, error) {
	var ctrl ControllerStatus
	err := s.db.QueryRowContext(ctx,
		`SELECT id, status, is_leader, started_at, last_heartbeat_at, config_revision, updated_at
		 FROM controller_status WHERE namespace = $1 ORDER BY updated_at DESC LIMIT 1`, ns).
		Scan(&ctrl.ID, &ctrl.Status, &ctrl.IsLeader, &ctrl.StartedAt, &ctrl.LastHeartbeatAt, &ctrl.ConfigRevision, &ctrl.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get controller: %w", err)
	}
	return &ctrl, nil
}

// Stale reaper (idempotent, lock-free)
// MarkStaleInstances marks gateway instances as "offline" whose updated_at is
// older than now()-threshold. Uses RETURNING to report exactly which rows changed.
// Idempotent: concurrent calls from multiple replicas are safe — the first one
// updates rows, subsequent calls match zero rows (status already "offline").
func (s *PgStore) MarkStaleInstances(ctx context.Context, threshold time.Duration) ([]StaleEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`UPDATE gateway_instances SET status = 'offline'
		 WHERE status != 'offline' AND updated_at < NOW() - $1::interval
		 RETURNING namespace, id`,
		threshold.String())
	if err != nil {
		return nil, fmt.Errorf("mark stale instances: %w", err)
	}
	defer rows.Close()

	var result []StaleEntry
	for rows.Next() {
		var e StaleEntry
		if err := rows.Scan(&e.Namespace, &e.ID); err != nil {
			return nil, fmt.Errorf("scan stale instance: %w", err)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// MarkStaleControllers marks controllers as "offline" whose updated_at is
// older than now()-threshold.
func (s *PgStore) MarkStaleControllers(ctx context.Context, threshold time.Duration) ([]StaleEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`UPDATE controller_status SET status = 'offline'
		 WHERE status != 'offline' AND updated_at < NOW() - $1::interval
		 RETURNING namespace, id`,
		threshold.String())
	if err != nil {
		return nil, fmt.Errorf("mark stale controllers: %w", err)
	}
	defer rows.Close()

	var result []StaleEntry
	for rows.Next() {
		var e StaleEntry
		if err := rows.Scan(&e.Namespace, &e.ID); err != nil {
			return nil, fmt.Errorf("scan stale controller: %w", err)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// Grafana dashboards (namespace-scoped)
func (s *PgStore) ListGrafanaDashboards(ctx context.Context, ns string) ([]GrafanaDashboard, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, url FROM grafana_dashboards WHERE namespace = $1 ORDER BY id`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list grafana dashboards: %w", err)
	}
	defer rows.Close()

	var result []GrafanaDashboard
	for rows.Next() {
		var d GrafanaDashboard
		if err := rows.Scan(&d.ID, &d.Name, &d.URL); err != nil {
			return nil, fmt.Errorf("pg scan grafana dashboard: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *PgStore) PutGrafanaDashboard(ctx context.Context, ns string, d *GrafanaDashboard) (*GrafanaDashboard, error) {
	if d.ID > 0 {
		_, err := s.db.ExecContext(ctx,
			`UPDATE grafana_dashboards SET name = $1, url = $2 WHERE id = $3 AND namespace = $4`,
			d.Name, d.URL, d.ID, ns)
		if err != nil {
			return nil, fmt.Errorf("pg update grafana dashboard: %w", err)
		}
		return d, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO grafana_dashboards (namespace, name, url) VALUES ($1, $2, $3) RETURNING id`,
		ns, d.Name, d.URL).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("pg insert grafana dashboard: %w", err)
	}
	d.ID = id
	return d, nil
}

func (s *PgStore) DeleteGrafanaDashboard(ctx context.Context, ns string, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM grafana_dashboards WHERE id = $1 AND namespace = $2`, id, ns)
	if err != nil {
		return fmt.Errorf("pg delete grafana dashboard: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("grafana dashboard %d not found", id)
	}
	return nil
}

// API Credentials (namespace-scoped, AK globally unique)
func (s *PgStore) ListAPICredentials(ctx context.Context, ns string) ([]APICredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, namespace, access_key, description, scopes, enabled, created_at, updated_at
		 FROM api_credentials WHERE namespace = $1 ORDER BY id`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list api credentials: %w", err)
	}
	defer rows.Close()

	var result []APICredential
	for rows.Next() {
		var c APICredential
		if err := rows.Scan(&c.ID, &c.Namespace, &c.AccessKey, &c.Description, pq.Array(&c.Scopes), &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("pg scan api credential: %w", err)
		}
		if c.Scopes == nil {
			c.Scopes = []string{}
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// GetAPICredentialByAK looks up a credential globally by access key (for HMAC auth).
func (s *PgStore) GetAPICredentialByAK(ctx context.Context, accessKey string) (*APICredential, error) {
	var c APICredential
	err := s.db.QueryRowContext(ctx,
		`SELECT id, namespace, access_key, secret_key, description, scopes, enabled, created_at, updated_at
		 FROM api_credentials WHERE access_key = $1`, accessKey).
		Scan(&c.ID, &c.Namespace, &c.AccessKey, &c.SecretKey, &c.Description, pq.Array(&c.Scopes), &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get api credential: %w", err)
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	return &c, nil
}

func (s *PgStore) CreateAPICredential(ctx context.Context, ns string, cred *APICredential) (*APICredential, error) {
	cred.Namespace = ns
	if cred.Scopes == nil {
		cred.Scopes = []string{}
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO api_credentials (namespace, access_key, secret_key, description, scopes, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at, updated_at`,
		ns, cred.AccessKey, cred.SecretKey, cred.Description, pq.Array(cred.Scopes), cred.Enabled).
		Scan(&cred.ID, &cred.CreatedAt, &cred.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("pg create api credential: %w", err)
	}
	return cred, nil
}

func (s *PgStore) UpdateAPICredential(ctx context.Context, ns string, cred *APICredential) error {
	if cred.Scopes == nil {
		cred.Scopes = []string{}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_credentials SET description = $1, enabled = $2, scopes = $3, updated_at = NOW()
		 WHERE id = $4 AND namespace = $5`,
		cred.Description, cred.Enabled, pq.Array(cred.Scopes), cred.ID, ns)
	if err != nil {
		return fmt.Errorf("pg update api credential: %w", err)
	}
	return nil
}

func (s *PgStore) DeleteAPICredential(ctx context.Context, ns string, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM api_credentials WHERE id = $1 AND namespace = $2`, id, ns)
	if err != nil {
		return fmt.Errorf("pg delete api credential: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api credential %d not found", id)
	}
	return nil
}

// Users (OIDC-synced)
func (s *PgStore) UpsertUser(ctx context.Context, user *User) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (sub, username, email, name, is_admin, must_change_password, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (sub) DO UPDATE SET
			username  = EXCLUDED.username,
			email     = EXCLUDED.email,
			name      = EXCLUDED.name,
			last_seen = NOW()`,
		user.Sub, user.Username, user.Email, user.Name, user.IsAdmin, user.MustChangePassword)
	if err != nil {
		return fmt.Errorf("pg upsert user: %w", err)
	}
	return nil
}

func (s *PgStore) GetUser(ctx context.Context, sub string) (*User, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT sub, username, email, name, is_admin, must_change_password, last_seen FROM users WHERE sub = $1`, sub).
		Scan(&u.Sub, &u.Username, &u.Email, &u.Name, &u.IsAdmin, &u.MustChangePassword, &u.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get user: %w", err)
	}
	return &u, nil
}

func (s *PgStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sub, username, email, name, is_admin, must_change_password, last_seen FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("pg list users: %w", err)
	}
	defer rows.Close()

	var result []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Sub, &u.Username, &u.Email, &u.Name, &u.IsAdmin, &u.MustChangePassword, &u.LastSeen); err != nil {
			return nil, fmt.Errorf("pg scan user: %w", err)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *PgStore) SetUserAdmin(ctx context.Context, sub string, isAdmin bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET is_admin = $1 WHERE sub = $2`, isAdmin, sub)
	if err != nil {
		return fmt.Errorf("pg set user admin: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (s *PgStore) GetUserPasswordHash(ctx context.Context, sub string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE sub = $1`, sub).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pg get password hash: %w", err)
	}
	return hash, nil
}

func (s *PgStore) UpdateUserPassword(ctx context.Context, sub, passwordHash string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $1 WHERE sub = $2`, passwordHash, sub)
	if err != nil {
		return fmt.Errorf("pg update password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (s *PgStore) SetMustChangePassword(ctx context.Context, sub string, must bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET must_change_password = $1 WHERE sub = $2`, must, sub)
	if err != nil {
		return fmt.Errorf("pg set must_change_password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (s *PgStore) DeleteUser(ctx context.Context, sub string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM users WHERE sub = $1`, sub)
	if err != nil {
		return fmt.Errorf("pg delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// JWT Signing Keys
func (s *PgStore) GetActiveSigningKey(ctx context.Context) (*JWTSigningKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var k JWTSigningKey
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT kid, secret, status, created_at, expires_at FROM jwt_signing_keys WHERE status = 'active' ORDER BY created_at DESC LIMIT 1`).
		Scan(&k.KID, &k.Secret, &k.Status, &k.CreatedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get active signing key: %w", err)
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Time
	}
	return &k, nil
}

func (s *PgStore) GetSigningKeyByID(ctx context.Context, kid string) (*JWTSigningKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var k JWTSigningKey
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT kid, secret, status, created_at, expires_at FROM jwt_signing_keys
		 WHERE kid = $1 AND (expires_at IS NULL OR expires_at > NOW())`, kid).
		Scan(&k.KID, &k.Secret, &k.Status, &k.CreatedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get signing key by id: %w", err)
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Time
	}
	return &k, nil
}

func (s *PgStore) ListValidSigningKeys(ctx context.Context) ([]JWTSigningKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kid, secret, status, created_at, expires_at FROM jwt_signing_keys
		 WHERE status = 'active' OR (status = 'retired' AND expires_at > NOW())
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("pg list valid signing keys: %w", err)
	}
	defer rows.Close()

	var keys []JWTSigningKey
	for rows.Next() {
		var k JWTSigningKey
		var expiresAt sql.NullTime
		if err := rows.Scan(&k.KID, &k.Secret, &k.Status, &k.CreatedAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("pg scan signing key: %w", err)
		}
		if expiresAt.Valid {
			k.ExpiresAt = &expiresAt.Time
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *PgStore) CreateSigningKey(ctx context.Context, key *JWTSigningKey) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO jwt_signing_keys (kid, secret, status, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (kid) DO NOTHING`,
		key.KID, key.Secret, key.Status, key.CreatedAt, key.ExpiresAt)
	if err != nil {
		return fmt.Errorf("pg create signing key: %w", err)
	}
	return nil
}

func (s *PgStore) RotateSigningKey(ctx context.Context, gracePeriod time.Duration) (*JWTSigningKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pg begin tx: %w", err)
	}
	defer tx.Rollback()

	// Retire all currently active keys with a grace period.
	expiresAt := time.Now().Add(gracePeriod)
	if _, err := tx.ExecContext(ctx,
		`UPDATE jwt_signing_keys SET status = 'retired', expires_at = $1 WHERE status = 'active'`,
		expiresAt); err != nil {
		return nil, fmt.Errorf("pg retire old keys: %w", err)
	}

	// Generate new active key.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	kid := generateKeyID()
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO jwt_signing_keys (kid, secret, status, created_at) VALUES ($1, $2, 'active', $3)`,
		kid, secret, now); err != nil {
		return nil, fmt.Errorf("pg insert new signing key: %w", err)
	}

	// Clean up expired keys (housekeeping).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM jwt_signing_keys WHERE status = 'retired' AND expires_at < NOW()`); err != nil {
		s.logger.Warnf("cleanup expired jwt keys: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("pg commit rotate key: %w", err)
	}

	return &JWTSigningKey{KID: kid, Secret: secret, Status: "active", CreatedAt: now}, nil
}

func generateKeyID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("k-%x", b)
}

// Namespace Members
func (s *PgStore) ListNamespaceMembers(ctx context.Context, ns string) ([]NamespaceMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.namespace, m.user_sub, m.role, u.username, u.email, u.name
		FROM namespace_members m
		JOIN users u ON u.sub = m.user_sub
		WHERE m.namespace = $1
		ORDER BY u.username`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list ns members: %w", err)
	}
	defer rows.Close()

	var result []NamespaceMember
	for rows.Next() {
		var m NamespaceMember
		if err := rows.Scan(&m.Namespace, &m.UserSub, &m.Role, &m.Username, &m.Email, &m.Name); err != nil {
			return nil, fmt.Errorf("pg scan ns member: %w", err)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *PgStore) GetNamespaceMember(ctx context.Context, ns, userSub string) (*NamespaceMember, error) {
	var m NamespaceMember
	err := s.db.QueryRowContext(ctx, `
		SELECT m.namespace, m.user_sub, m.role, u.username, u.email, u.name
		FROM namespace_members m
		JOIN users u ON u.sub = m.user_sub
		WHERE m.namespace = $1 AND m.user_sub = $2`, ns, userSub).
		Scan(&m.Namespace, &m.UserSub, &m.Role, &m.Username, &m.Email, &m.Name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pg get ns member: %w", err)
	}
	return &m, nil
}

func (s *PgStore) SetNamespaceMember(ctx context.Context, ns, userSub string, role NamespaceRole) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO namespace_members (namespace, user_sub, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (namespace, user_sub) DO UPDATE SET role = EXCLUDED.role`,
		ns, userSub, string(role))
	if err != nil {
		return fmt.Errorf("pg set ns member: %w", err)
	}
	return nil
}

func (s *PgStore) RemoveNamespaceMember(ctx context.Context, ns, userSub string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM namespace_members WHERE namespace = $1 AND user_sub = $2`, ns, userSub)
	if err != nil {
		return fmt.Errorf("pg remove ns member: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member not found")
	}
	return nil
}

// Group Bindings (OIDC group → namespace role)

func (s *PgStore) ListGroupBindings(ctx context.Context, ns string) ([]GroupBinding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, namespace, group_name, role FROM group_bindings WHERE namespace = $1 ORDER BY group_name`, ns)
	if err != nil {
		return nil, fmt.Errorf("pg list group bindings: %w", err)
	}
	defer rows.Close()

	var result []GroupBinding
	for rows.Next() {
		var b GroupBinding
		if err := rows.Scan(&b.ID, &b.Namespace, &b.Group, &b.Role); err != nil {
			return nil, fmt.Errorf("pg scan group binding: %w", err)
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (s *PgStore) SetGroupBinding(ctx context.Context, ns, group string, role NamespaceRole) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO group_bindings (namespace, group_name, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (namespace, group_name) DO UPDATE SET role = EXCLUDED.role`,
		ns, group, string(role))
	if err != nil {
		return fmt.Errorf("pg set group binding: %w", err)
	}
	return nil
}

func (s *PgStore) RemoveGroupBinding(ctx context.Context, ns, group string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM group_bindings WHERE namespace = $1 AND group_name = $2`, ns, group)
	if err != nil {
		return fmt.Errorf("pg remove group binding: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group binding not found")
	}
	return nil
}

// GetEffectiveRoleByGroups returns the highest-privilege role among all bindings for the given groups.
// Role priority: owner > editor > viewer. Returns nil if no binding matches.
func (s *PgStore) GetEffectiveRoleByGroups(ctx context.Context, ns string, groups []string) (*NamespaceRole, error) {
	if len(groups) == 0 {
		return nil, nil
	}

	// Build placeholders for the group list.
	args := []any{ns}
	placeholders := ""
	for i, g := range groups {
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", i+2)
		args = append(args, g)
	}

	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT role FROM group_bindings WHERE namespace = $1 AND group_name IN (%s)`, placeholders),
		args...)
	if err != nil {
		return nil, fmt.Errorf("pg get effective role by groups: %w", err)
	}
	defer rows.Close()

	var best *NamespaceRole
	for rows.Next() {
		var role NamespaceRole
		if err := rows.Scan(&role); err != nil {
			return nil, fmt.Errorf("pg scan group role: %w", err)
		}
		if best == nil || RolePriority(role) > RolePriority(*best) {
			best = &role
		}
	}
	return best, rows.Err()
}

// RolePriority returns numeric priority for role comparison.
func RolePriority(r NamespaceRole) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}
