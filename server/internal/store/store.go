package store

import (
	"context"
	"regexp"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/model"
)

// DefaultNamespace is used when no namespace is specified.
const DefaultNamespace = "default"

// namespaceRe matches valid namespace names: lowercase alphanumeric, hyphens,
// 1-63 characters, must start and end with alphanumeric.
var namespaceRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateNamespaceName returns an error message if the name is invalid, or "" if valid.
func ValidateNamespaceName(name string) string {
	if name == "" {
		return "namespace name is required"
	}
	if len(name) > 63 {
		return "namespace name must be at most 63 characters"
	}
	if !namespaceRe.MatchString(name) {
		return "namespace name must consist of lowercase alphanumeric characters or hyphens, and must start and end with an alphanumeric character"
	}
	return ""
}

// HistoryEntry records a single version of one domain or cluster.
type HistoryEntry struct {
	Version   int64                `json:"version"`
	Timestamp time.Time            `json:"timestamp"`
	Kind      string               `json:"kind"` // "domain" or "cluster"
	Name      string               `json:"name"`
	Action    string               `json:"action"` // "create", "update", "delete", "rollback", "import"
	Operator  string               `json:"operator,omitempty"`
	Domain    *model.DomainConfig  `json:"domain,omitempty"`
	Cluster   *model.ClusterConfig `json:"cluster,omitempty"`
}

// Store is the interface that both handlers and the watch API depend on.
// All data methods are namespace-scoped.
type Store interface {
	Close()

	// ── Domain CRUD ─────────────────────────────
	ListDomains(ctx context.Context, ns string) ([]model.DomainConfig, error)
	GetDomain(ctx context.Context, ns, name string) (*model.DomainConfig, error)
	PutDomain(ctx context.Context, ns string, domain *model.DomainConfig, action, operator string) (int64, error)
	DeleteDomain(ctx context.Context, ns, name, operator string) (int64, error)

	// ── Cluster CRUD ───────────────────────────
	ListClusters(ctx context.Context, ns string) ([]model.ClusterConfig, error)
	GetCluster(ctx context.Context, ns, name string) (*model.ClusterConfig, error)
	PutCluster(ctx context.Context, ns string, cluster *model.ClusterConfig, action, operator string) (int64, error)
	DeleteCluster(ctx context.Context, ns, name, operator string) (int64, error)

	// ── Bulk ───────────────────────────────────
	PutAllConfig(ctx context.Context, ns string, domains []model.DomainConfig, clusters []model.ClusterConfig, operator string) (int64, error)
	GetConfig(ctx context.Context, ns string) (*model.GatewayConfig, error)

	// ── Per-domain History ──────────────────────
	GetDomainHistory(ctx context.Context, ns, name string) ([]HistoryEntry, error)
	GetDomainVersion(ctx context.Context, ns, name string, version int64) (*HistoryEntry, error)
	RollbackDomain(ctx context.Context, ns, name string, version int64, operator string) (int64, error)

	// ── Per-cluster History ────────────────────
	GetClusterHistory(ctx context.Context, ns, name string) ([]HistoryEntry, error)
	GetClusterVersion(ctx context.Context, ns, name string, version int64) (*HistoryEntry, error)
	RollbackCluster(ctx context.Context, ns, name string, version int64, operator string) (int64, error)

	// ── Audit log (global change event stream) ─
	ListAuditLog(ctx context.Context, ns string, limit, offset int) ([]AuditEntry, int64, error)
	InsertAuditLog(ctx context.Context, ns, kind, name, action, operator string) error

	// ── Watch (for controller long-poll) ───────
	CurrentRevision(ctx context.Context, ns string) (int64, error)
	WatchFrom(ctx context.Context, ns string, sinceRevision int64) ([]ChangeEvent, int64, error)

	// ── Namespaces ──────────────────────────────
	ListNamespaces(ctx context.Context) ([]string, error)
	CreateNamespace(ctx context.Context, name string) error

	// ── Status (namespace-scoped) ──────────────────
	UpsertGatewayInstances(ctx context.Context, ns string, instances []GatewayInstanceStatus) error
	ListGatewayInstances(ctx context.Context, ns string) ([]GatewayInstanceStatus, error)
	UpsertControllerStatus(ctx context.Context, ns string, ctrl *ControllerStatus) error
	GetControllerStatus(ctx context.Context, ns string) (*ControllerStatus, error)

	// ── Stale instance/controller reaper ─────────
	// MarkStaleInstances marks gateway instances as "offline" if their updated_at
	// is older than the given threshold. Returns the list of newly-offlined entries.
	// Idempotent: multiple replicas can call concurrently without conflict.
	MarkStaleInstances(ctx context.Context, threshold time.Duration) ([]StaleEntry, error)
	// MarkStaleControllers marks controllers as "offline" if their updated_at
	// is older than the given threshold. Same idempotent semantics.
	MarkStaleControllers(ctx context.Context, threshold time.Duration) ([]StaleEntry, error)

	// ── Grafana dashboards (namespace-scoped) ─────
	ListGrafanaDashboards(ctx context.Context, ns string) ([]GrafanaDashboard, error)
	PutGrafanaDashboard(ctx context.Context, ns string, d *GrafanaDashboard) (*GrafanaDashboard, error)
	DeleteGrafanaDashboard(ctx context.Context, ns string, id int64) error

	// ── API Credentials (namespace-scoped) ────────
	ListAPICredentials(ctx context.Context, ns string) ([]APICredential, error)
	GetAPICredentialByAK(ctx context.Context, accessKey string) (*APICredential, error) // auth lookup is global (AK is globally unique)
	CreateAPICredential(ctx context.Context, ns string, cred *APICredential) (*APICredential, error)
	UpdateAPICredential(ctx context.Context, ns string, cred *APICredential) error
	DeleteAPICredential(ctx context.Context, ns string, id int64) error

	// ── Users (OIDC-synced) ───────────────────────
	UpsertUser(ctx context.Context, user *User) error // INSERT sets is_admin; UPDATE preserves existing
	GetUser(ctx context.Context, sub string) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	SetUserAdmin(ctx context.Context, sub string, isAdmin bool) error

	// ── Namespace Members ─────────────────────────
	ListNamespaceMembers(ctx context.Context, ns string) ([]NamespaceMember, error)
	GetNamespaceMember(ctx context.Context, ns, userSub string) (*NamespaceMember, error)
	SetNamespaceMember(ctx context.Context, ns, userSub string, role NamespaceRole) error
	RemoveNamespaceMember(ctx context.Context, ns, userSub string) error

	// ── Group Bindings (OIDC group → namespace role) ─
	ListGroupBindings(ctx context.Context, ns string) ([]GroupBinding, error)
	SetGroupBinding(ctx context.Context, ns, group string, role NamespaceRole) error
	RemoveGroupBinding(ctx context.Context, ns, group string) error
	// GetEffectiveRoleByGroups returns the highest-privilege role granted to any of the given groups in a namespace.
	GetEffectiveRoleByGroups(ctx context.Context, ns string, groups []string) (*NamespaceRole, error)
}

// ChangeEvent represents a single config change for the watch API.
type ChangeEvent struct {
	Revision  int64                `json:"revision"`
	Kind      string               `json:"kind"` // "domain" or "cluster"
	Name      string               `json:"name"`
	Action    string               `json:"action"` // "create", "update", "delete", "rollback", "import"
	Operator  string               `json:"operator,omitempty"`
	Domain    *model.DomainConfig  `json:"domain,omitempty"`
	Cluster   *model.ClusterConfig `json:"cluster,omitempty"`
}

// AuditEntry represents a global change event for audit purposes.
type AuditEntry struct {
	Revision  int64     `json:"revision"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Action    string    `json:"action"`
	Operator  string    `json:"operator,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ── Status (shared across replicas) ──────────────

// GatewayInstanceStatus is the status of a single gateway instance.
type GatewayInstanceStatus struct {
	ID              string    `json:"id"`
	Status          string    `json:"status,omitempty"`
	StartedAt       string    `json:"started_at,omitempty"`
	RegisteredAt    string    `json:"registered_at,omitempty"`
	LastKeepaliveAt string    `json:"last_keepalive_at,omitempty"`
	ConfigRevision  int64     `json:"config_revision,omitempty"`
	LastSeenAt      string    `json:"last_seen_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ControllerStatus is the status of the controller.
type ControllerStatus struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	StartedAt       string    `json:"started_at"`
	LastHeartbeatAt string    `json:"last_heartbeat_at"`
	ConfigRevision  int64     `json:"config_revision"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StaleEntry identifies a component that was marked offline by the reaper.
type StaleEntry struct {
	Namespace string `json:"namespace"`
	ID        string `json:"id"`
}

// ── Settings (shared across replicas) ────────────

// GrafanaDashboard is a persisted Grafana dashboard configuration.
type GrafanaDashboard struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ── API Credentials (AK/SK for service-to-service auth) ─

// Scope constants define fine-grained permissions for API credentials.
const (
	ScopeConfigRead      = "config:read"
	ScopeConfigWrite     = "config:write"
	ScopeConfigWatch     = "config:watch"
	ScopeStatusRead      = "status:read"
	ScopeStatusWrite     = "status:write"
	ScopeCredentialRead  = "credential:read"
	ScopeCredentialWrite = "credential:write"
	ScopeMemberRead      = "member:read"
	ScopeMemberWrite     = "member:write"
	ScopeAuditRead       = "audit:read"
	ScopeAdminUsers      = "admin:users"
	ScopeNamespaceRead   = "namespace:read"
	ScopeNamespaceWrite  = "namespace:write"
)

// AllScopes is the complete list of valid scopes.
var AllScopes = []string{
	ScopeConfigRead, ScopeConfigWrite, ScopeConfigWatch,
	ScopeStatusRead, ScopeStatusWrite,
	ScopeCredentialRead, ScopeCredentialWrite,
	ScopeMemberRead, ScopeMemberWrite,
	ScopeAuditRead,
	ScopeAdminUsers,
	ScopeNamespaceRead, ScopeNamespaceWrite,
}

// RoleToScopes maps an OIDC user's namespace role to the equivalent scope set.
func RoleToScopes(role NamespaceRole, isAdmin bool) []string {
	if isAdmin {
		return AllScopes
	}
	switch role {
	case RoleOwner:
		return []string{
			ScopeConfigRead, ScopeConfigWrite, ScopeConfigWatch,
			ScopeStatusRead, ScopeStatusWrite,
			ScopeCredentialRead, ScopeCredentialWrite,
			ScopeMemberRead, ScopeMemberWrite,
			ScopeAuditRead,
			ScopeNamespaceRead, ScopeNamespaceWrite,
		}
	case RoleEditor:
		return []string{
			ScopeConfigRead, ScopeConfigWrite,
			ScopeStatusRead,
			ScopeCredentialRead, ScopeCredentialWrite,
			ScopeMemberRead, ScopeMemberWrite,
			ScopeAuditRead,
			ScopeNamespaceRead,
		}
	case RoleViewer:
		return []string{
			ScopeConfigRead,
			ScopeStatusRead,
			ScopeCredentialRead,
			ScopeMemberRead,
			ScopeAuditRead,
			ScopeNamespaceRead,
		}
	default:
		return nil
	}
}

// ValidScope returns true if s is a known scope.
func ValidScope(s string) bool {
	for _, sc := range AllScopes {
		if sc == s {
			return true
		}
	}
	return false
}

// APICredential represents a managed AK/SK pair for HMAC-SHA256 authentication.
// Credentials are namespace-scoped; AK is globally unique for auth lookup.
type APICredential struct {
	ID          int64     `json:"id"`
	Namespace   string    `json:"namespace,omitempty"`
	AccessKey   string    `json:"access_key"`
	SecretKey   string    `json:"secret_key,omitempty"` // omitted on list for safety; only returned on create
	Description string    `json:"description"`
	Scopes      []string  `json:"scopes"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// HasScope returns true if the credential includes the given scope.
func (c *APICredential) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ── Users (synced from OIDC) ─────────────────────

// User represents a user synced from the OIDC provider.
type User struct {
	Sub      string    `json:"sub"`
	Username string    `json:"username"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	IsAdmin  bool      `json:"is_admin"`
	LastSeen time.Time `json:"last_seen"`
}

// GroupBinding maps an OIDC group to a role within a namespace.
type GroupBinding struct {
	ID        int64         `json:"id"`
	Namespace string        `json:"namespace"`
	Group     string        `json:"group"`
	Role      NamespaceRole `json:"role"`
}

// NamespaceRole defines the permission level of a user within a namespace.
type NamespaceRole string

const (
	RoleOwner  NamespaceRole = "owner"
	RoleEditor NamespaceRole = "editor"
	RoleViewer NamespaceRole = "viewer"
)

// NamespaceMember represents a user's role within a namespace.
type NamespaceMember struct {
	Namespace string        `json:"namespace"`
	UserSub   string        `json:"user_sub"`
	Role      NamespaceRole `json:"role"`
	Username  string        `json:"username,omitempty"` // joined from users table
	Email     string        `json:"email,omitempty"`
	Name      string        `json:"name,omitempty"`
}
