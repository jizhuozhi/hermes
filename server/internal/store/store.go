package store

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/model"
)

// ErrConflict is returned when an optimistic concurrency check fails.
// The caller provided an expectedVersion that doesn't match the current
// resource_version in the database, indicating another user has modified
// the resource concurrently.
var ErrConflict = errors.New("optimistic concurrency conflict: resource has been modified by another user")

// DefaultRegion is used when no region is specified.
const DefaultRegion = "default"

// regionRe matches valid region names: lowercase alphanumeric, hyphens,
// 1-63 characters, must start and end with alphanumeric.
var regionRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateRegionName returns an error message if the name is invalid, or "" if valid.
func ValidateRegionName(name string) string {
	if name == "" {
		return "region name is required"
	}
	if len(name) > 63 {
		return "region name must be at most 63 characters"
	}
	if !regionRe.MatchString(name) {
		return "region name must consist of lowercase alphanumeric characters or hyphens, and must start and end with an alphanumeric character"
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
// All data methods are region-scoped.
type Store interface {
	Close()

	// Domain CRUD
	ListDomains(ctx context.Context, region string) ([]model.DomainConfig, error)
	GetDomain(ctx context.Context, region, name string) (*model.DomainConfig, int64, error) // returns (config, resourceVersion, err)
	PutDomain(ctx context.Context, region string, domain *model.DomainConfig, action, operator string, expectedVersion int64) (int64, error)
	DeleteDomain(ctx context.Context, region, name, operator string) (int64, error)

	// Cluster CRUD
	ListClusters(ctx context.Context, region string) ([]model.ClusterConfig, error)
	GetCluster(ctx context.Context, region, name string) (*model.ClusterConfig, int64, error) // returns (config, resourceVersion, err)
	PutCluster(ctx context.Context, region string, cluster *model.ClusterConfig, action, operator string, expectedVersion int64) (int64, error)
	DeleteCluster(ctx context.Context, region, name, operator string) (int64, error)

	// Bulk
	PutAllConfig(ctx context.Context, region string, domains []model.DomainConfig, clusters []model.ClusterConfig, operator string) (int64, error)
	GetConfig(ctx context.Context, region string) (*model.GatewayConfig, error)

	// Per-domain History
	GetDomainHistory(ctx context.Context, region, name string) ([]HistoryEntry, error)
	GetDomainVersion(ctx context.Context, region, name string, version int64) (*HistoryEntry, error)
	RollbackDomain(ctx context.Context, region, name string, version int64, operator string) (int64, error)

	// Per-cluster History
	GetClusterHistory(ctx context.Context, region, name string) ([]HistoryEntry, error)
	GetClusterVersion(ctx context.Context, region, name string, version int64) (*HistoryEntry, error)
	RollbackCluster(ctx context.Context, region, name string, version int64, operator string) (int64, error)

	// Audit log (global change event stream)
	ListAuditLog(ctx context.Context, region string, limit, offset int) ([]AuditEntry, int64, error)
	InsertAuditLog(ctx context.Context, region, kind, name, action, operator string) error

	// Watch (for controller long-poll)
	CurrentRevision(ctx context.Context, region string) (int64, error)
	WatchFrom(ctx context.Context, region string, sinceRevision int64) ([]ChangeEvent, int64, error)

	// Regions
	ListRegions(ctx context.Context) ([]string, error)
	CreateRegion(ctx context.Context, name string) error

	// Status (region-scoped)
	UpsertGatewayInstances(ctx context.Context, region string, instances []GatewayInstanceStatus) error
	ListGatewayInstances(ctx context.Context, region string) ([]GatewayInstanceStatus, error)
	UpsertControllerStatus(ctx context.Context, region string, ctrl *ControllerStatus) error
	GetControllerStatus(ctx context.Context, region string) (*ControllerStatus, error)

	// Stale instance/controller reaper
	// MarkStaleInstances marks gateway instances as "offline" if their updated_at
	// is older than the given threshold. Returns the list of newly-offlined entries.
	// Idempotent: multiple replicas can call concurrently without conflict.
	MarkStaleInstances(ctx context.Context, threshold time.Duration) ([]StaleEntry, error)
	// MarkStaleControllers marks controllers as "offline" if their updated_at
	// is older than the given threshold. Same idempotent semantics.
	MarkStaleControllers(ctx context.Context, threshold time.Duration) ([]StaleEntry, error)

	// Grafana dashboards (region-scoped)
	ListGrafanaDashboards(ctx context.Context, region string) ([]GrafanaDashboard, error)
	PutGrafanaDashboard(ctx context.Context, region string, d *GrafanaDashboard) (*GrafanaDashboard, error)
	DeleteGrafanaDashboard(ctx context.Context, region string, id int64) error

	// API Credentials (region-scoped)
	ListAPICredentials(ctx context.Context, region string) ([]APICredential, error)
	GetAPICredentialByAK(ctx context.Context, accessKey string) (*APICredential, error) // auth lookup is global (AK is globally unique)
	CreateAPICredential(ctx context.Context, region string, cred *APICredential) (*APICredential, error)
	UpdateAPICredential(ctx context.Context, region string, cred *APICredential) error
	DeleteAPICredential(ctx context.Context, region string, id int64) error

	// Users (OIDC-synced or builtin)
	UpsertUser(ctx context.Context, user *User) error // INSERT sets is_admin; UPDATE preserves existing
	GetUser(ctx context.Context, sub string) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	SetUserAdmin(ctx context.Context, sub string, isAdmin bool) error
	// GetUserPasswordHash returns the bcrypt hash for builtin auth (empty if not set).
	GetUserPasswordHash(ctx context.Context, sub string) (string, error)
	// UpdateUserPassword sets the password hash for a builtin user.
	UpdateUserPassword(ctx context.Context, sub, passwordHash string) error
	// SetMustChangePassword sets or clears the must_change_password flag for a user.
	SetMustChangePassword(ctx context.Context, sub string, must bool) error
	// DeleteUser removes a user by sub. Returns error if not found.
	DeleteUser(ctx context.Context, sub string) error

	// JWT Signing Keys (builtin auth)
	// GetActiveSigningKey returns the current active key for token signing.
	// Returns nil if no active key exists.
	GetActiveSigningKey(ctx context.Context) (*JWTSigningKey, error)
	// GetSigningKeyByID returns a key by its kid. Used for token verification.
	GetSigningKeyByID(ctx context.Context, kid string) (*JWTSigningKey, error)
	// ListValidSigningKeys returns all keys that are active or not yet expired.
	// Used as fallback when kid is missing from a token.
	ListValidSigningKeys(ctx context.Context) ([]JWTSigningKey, error)
	// CreateSigningKey inserts a new signing key. If an active key exists,
	// it is retired with the given grace period.
	CreateSigningKey(ctx context.Context, key *JWTSigningKey) error
	// RotateSigningKey creates a new active key and retires the old one.
	// The old key remains valid for gracePeriod (so in-flight tokens don't break).
	RotateSigningKey(ctx context.Context, gracePeriod time.Duration) (*JWTSigningKey, error)

	// Region Members
	ListRegionMembers(ctx context.Context, region string) ([]RegionMember, error)
	GetRegionMember(ctx context.Context, region, userSub string) (*RegionMember, error)
	SetRegionMember(ctx context.Context, region, userSub string, role RegionRole) error
	RemoveRegionMember(ctx context.Context, region, userSub string) error

	// Group Bindings (OIDC group → region role)
	ListGroupBindings(ctx context.Context, region string) ([]GroupBinding, error)
	SetGroupBinding(ctx context.Context, region, group string, role RegionRole) error
	RemoveGroupBinding(ctx context.Context, region, group string) error
	// GetEffectiveRoleByGroups returns the highest-privilege role granted to any of the given groups in a region.
	GetEffectiveRoleByGroups(ctx context.Context, region string, groups []string) (*RegionRole, error)
}

// ChangeEvent represents a single config change for the watch API.
type ChangeEvent struct {
	Revision int64                `json:"revision"`
	Kind     string               `json:"kind"` // "domain" or "cluster"
	Name     string               `json:"name"`
	Action   string               `json:"action"` // "create", "update", "delete", "rollback", "import"
	Operator string               `json:"operator,omitempty"`
	Domain   *model.DomainConfig  `json:"domain,omitempty"`
	Cluster  *model.ClusterConfig `json:"cluster,omitempty"`
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

// Status (shared across replicas)
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
	IsLeader        bool      `json:"is_leader"`
	StartedAt       string    `json:"started_at"`
	LastHeartbeatAt string    `json:"last_heartbeat_at"`
	ConfigRevision  int64     `json:"config_revision"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StaleEntry identifies a component that was marked offline by the reaper.
type StaleEntry struct {
	Region string `json:"region"`
	ID     string `json:"id"`
}

// Settings (shared across replicas)
// GrafanaDashboard is a persisted Grafana dashboard configuration.
type GrafanaDashboard struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// API Credentials (AK/SK for service-to-service auth)

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
	ScopeRegionRead      = "region:read"
	ScopeRegionWrite     = "region:write"
)

// AllScopes is the complete list of valid scopes.
var AllScopes = []string{
	ScopeConfigRead, ScopeConfigWrite, ScopeConfigWatch,
	ScopeStatusRead, ScopeStatusWrite,
	ScopeCredentialRead, ScopeCredentialWrite,
	ScopeMemberRead, ScopeMemberWrite,
	ScopeAuditRead,
	ScopeAdminUsers,
	ScopeRegionRead, ScopeRegionWrite,
}

// RoleToScopes maps an OIDC user's region role to the equivalent scope set.
func RoleToScopes(role RegionRole, isAdmin bool) []string {
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
			ScopeRegionRead, ScopeRegionWrite,
		}
	case RoleEditor:
		return []string{
			ScopeConfigRead, ScopeConfigWrite,
			ScopeStatusRead,
			ScopeCredentialRead, ScopeCredentialWrite,
			ScopeMemberRead, ScopeMemberWrite,
			ScopeAuditRead,
			ScopeRegionRead,
		}
	case RoleViewer:
		return []string{
			ScopeConfigRead,
			ScopeStatusRead,
			ScopeCredentialRead,
			ScopeMemberRead,
			ScopeAuditRead,
			ScopeRegionRead,
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
// Credentials are region-scoped; AK is globally unique for auth lookup.
type APICredential struct {
	ID          int64     `json:"id"`
	Region      string    `json:"region,omitempty"`
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

// JWT Signing Keys (builtin auth)
// JWTSigningKey represents a persistent HMAC-SHA256 signing key for builtin auth.
// Keys have a lifecycle: active → retired → expired (deleted by reaper).
type JWTSigningKey struct {
	KID       string     `json:"kid"`    // unique key identifier, included in JWT header
	Secret    []byte     `json:"-"`      // raw 256-bit HMAC key (never serialized to JSON)
	Status    string     `json:"status"` // "active" or "retired"
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil for active key; set when retired
}

// Users (synced from OIDC)
// User represents a user synced from the OIDC provider.
type User struct {
	Sub                string    `json:"sub"`
	Username           string    `json:"username"`
	Email              string    `json:"email"`
	Name               string    `json:"name"`
	IsAdmin            bool      `json:"is_admin"`
	MustChangePassword bool      `json:"must_change_password"`
	LastSeen           time.Time `json:"last_seen"`
}

// GroupBinding maps an OIDC group to a role within a region.
type GroupBinding struct {
	ID     int64      `json:"id"`
	Region string     `json:"region"`
	Group  string     `json:"group"`
	Role   RegionRole `json:"role"`
}

// RegionRole defines the permission level of a user within a region.
type RegionRole string

const (
	RoleOwner  RegionRole = "owner"
	RoleEditor RegionRole = "editor"
	RoleViewer RegionRole = "viewer"
)

// RegionMember represents a user's role within a region.
type RegionMember struct {
	Region   string     `json:"region"`
	UserSub  string     `json:"user_sub"`
	Role     RegionRole `json:"role"`
	Username string     `json:"username,omitempty"` // joined from users table
	Email    string     `json:"email,omitempty"`
	Name     string     `json:"name,omitempty"`
}
