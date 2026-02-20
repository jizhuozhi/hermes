package main

import (
	"context"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jizhuozhi/hermes/server/internal/config"
	"github.com/jizhuozhi/hermes/server/internal/handler"
	"github.com/jizhuozhi/hermes/server/internal/store"

	"go.uber.org/zap"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "config file path")
	flag.Parse()

	logger, _ := zap.NewProduction()
	defer logger.Sync()
	sugar := logger.Sugar()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	pgStore, err := store.NewPgStore(cfg.Postgres.DSN, sugar)
	if err != nil {
		log.Fatalf("failed to connect postgres: %v", err)
	}
	defer pgStore.Close()

	domainHandler := handler.NewDomainHandler(pgStore, sugar)
	configHandler := handler.NewRouteHandler(pgStore, sugar)
	clusterHandler := handler.NewClusterHandler(pgStore, sugar)
	watchHandler := handler.NewWatchHandler(pgStore, sugar)
	statusHandler := handler.NewStatusHandler(pgStore, sugar)
	auditHandler := handler.NewAuditHandler(pgStore, sugar)
	grafanaHandler := handler.NewGrafanaHandler(pgStore, sugar)
	credentialHandler := handler.NewCredentialHandler(pgStore, sugar)
	memberHandler := handler.NewMemberHandler(pgStore, sugar)

	// OIDC handler (auth endpoints are always registered; verifier is conditional).
	var oidcHandler *handler.OIDCHandler
	var builtinHandler *handler.BuiltinAuthHandler
	var oidcVerifier handler.OIDCVerifyFunc

	switch cfg.AuthMode {
	case "oidc":
		var err error
		oidcHandler, err = handler.NewOIDCHandler(cfg.OIDC, pgStore, sugar)
		if err != nil {
			sugar.Fatalf("OIDC init failed: %v", err)
		}
		oidcVerifier = handler.NewOIDCVerifier(cfg.OIDC, oidcHandler.JwksURI())
		sugar.Infof("OIDC authentication enabled (issuer=%s, client_id=%s)", cfg.OIDC.Issuer, cfg.OIDC.ClientID)

	case "builtin":
		var err error
		builtinHandler, err = handler.NewBuiltinAuthHandler(cfg.BuiltinAuth, pgStore, sugar)
		if err != nil {
			sugar.Fatalf("Builtin auth init failed: %v", err)
		}
		oidcVerifier = handler.NewBuiltinVerifier(pgStore)
		sugar.Info("Built-in authentication enabled")

	default:
		sugar.Info("Authentication disabled (no auth_mode configured)")
	}

	// Middleware factories
	nsMW := handler.NamespaceMiddleware
	authMW := handler.Authenticate(pgStore, oidcVerifier, sugar)

	// Scope shortcuts.
	configRead := handler.RequireScope(store.ScopeConfigRead)
	configWrite := handler.RequireScope(store.ScopeConfigWrite)
	configWatch := handler.RequireScope(store.ScopeConfigWatch)
	statusRead := handler.RequireScope(store.ScopeStatusRead)
	statusWrite := handler.RequireScope(store.ScopeStatusWrite)
	credRead := handler.RequireScope(store.ScopeCredentialRead)
	credWrite := handler.RequireScope(store.ScopeCredentialWrite)
	memberRead := handler.RequireScope(store.ScopeMemberRead)
	memberWrite := handler.RequireScope(store.ScopeMemberWrite)
	auditRead := handler.RequireScope(store.ScopeAuditRead)
	adminUsers := handler.RequireScope(store.ScopeAdminUsers)
	nsRead := handler.RequireScope(store.ScopeNamespaceRead)
	nsWrite := handler.RequireScope(store.ScopeNamespaceWrite)

	mux := http.NewServeMux()

	// Public: Auth API (no authentication required)
	mux.HandleFunc("GET /api/auth/config", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"enabled": false}
		switch cfg.AuthMode {
		case "oidc":
			resp["enabled"] = true
			resp["mode"] = "oidc"
		case "builtin":
			resp["enabled"] = true
			resp["mode"] = "builtin"
		}
		handler.JSON(w, http.StatusOK, resp)
	})
	if oidcHandler != nil {
		mux.HandleFunc("GET /api/auth/login", oidcHandler.Login)
		mux.HandleFunc("GET /api/auth/token", oidcHandler.Callback)
		mux.HandleFunc("POST /api/auth/refresh", oidcHandler.Refresh)
		mux.Handle("GET /api/auth/userinfo", handler.Wrap(http.HandlerFunc(oidcHandler.Userinfo), nsMW, authMW))
	}
	if builtinHandler != nil {
		mux.HandleFunc("POST /api/auth/login", builtinHandler.Login)
		mux.Handle("GET /api/auth/userinfo", handler.Wrap(http.HandlerFunc(builtinHandler.Userinfo), nsMW, authMW))
		mux.Handle("POST /api/auth/change-password", handler.Wrap(http.HandlerFunc(builtinHandler.ChangePassword), nsMW, authMW))
		mux.Handle("POST /api/auth/rotate-key", handler.Wrap(http.HandlerFunc(builtinHandler.RotateKey), authMW, adminUsers))
	}

	// Scopes reference (public)
	mux.HandleFunc("GET /api/v1/scopes", func(w http.ResponseWriter, r *http.Request) {
		handler.JSON(w, http.StatusOK, map[string]any{"scopes": store.AllScopes})
	})

	// Authenticated API: unified /api/v1/
	// All endpoints below require authentication (OIDC Bearer or HMAC-SHA256).
	// Authorization is scope-based: each endpoint checks RequireScope.

	// -- WhoAmI (any authenticated caller) --
	mux.Handle("GET /api/v1/whoami", handler.Wrap(http.HandlerFunc(memberHandler.WhoAmI), nsMW, authMW))

	// -- Config read (viewer+ / credential with config:read) --
	mux.Handle("GET /api/v1/config", handler.Wrap(http.HandlerFunc(configHandler.GetConfig), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/config/revision", handler.Wrap(http.HandlerFunc(watchHandler.GetRevision), nsMW, authMW, configRead))
	mux.Handle("POST /api/v1/config/validate", handler.Wrap(http.HandlerFunc(configHandler.ValidateConfig), nsMW, authMW, configRead))

	// -- Config watch (controller / credential with config:watch) --
	mux.Handle("GET /api/v1/config/watch", handler.Wrap(http.HandlerFunc(watchHandler.WatchConfig), nsMW, authMW, configWatch))

	// -- Config write (editor+ / credential with config:write) --
	mux.Handle("PUT /api/v1/config", handler.Wrap(http.HandlerFunc(configHandler.PutConfig), nsMW, authMW, configWrite))

	// -- Domains --
	mux.Handle("GET /api/v1/domains", handler.Wrap(http.HandlerFunc(domainHandler.ListDomains), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/domains/{name}", handler.Wrap(http.HandlerFunc(domainHandler.GetDomain), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/domains/{name}/history", handler.Wrap(http.HandlerFunc(domainHandler.ListDomainHistory), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/domains/{name}/history/{version}", handler.Wrap(http.HandlerFunc(domainHandler.GetDomainVersion), nsMW, authMW, configRead))
	mux.Handle("POST /api/v1/domains", handler.Wrap(http.HandlerFunc(domainHandler.CreateDomain), nsMW, authMW, configWrite))
	mux.Handle("PUT /api/v1/domains/{name}", handler.Wrap(http.HandlerFunc(domainHandler.UpdateDomain), nsMW, authMW, configWrite))
	mux.Handle("DELETE /api/v1/domains/{name}", handler.Wrap(http.HandlerFunc(domainHandler.DeleteDomain), nsMW, authMW, configWrite))
	mux.Handle("POST /api/v1/domains/{name}/rollback/{version}", handler.Wrap(http.HandlerFunc(domainHandler.RollbackDomain), nsMW, authMW, configWrite))

	// -- Clusters --
	mux.Handle("GET /api/v1/clusters", handler.Wrap(http.HandlerFunc(clusterHandler.ListClusters), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/clusters/{name}", handler.Wrap(http.HandlerFunc(clusterHandler.GetCluster), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/clusters/{name}/history", handler.Wrap(http.HandlerFunc(clusterHandler.ListClusterHistory), nsMW, authMW, configRead))
	mux.Handle("GET /api/v1/clusters/{name}/history/{version}", handler.Wrap(http.HandlerFunc(clusterHandler.GetClusterVersion), nsMW, authMW, configRead))
	mux.Handle("POST /api/v1/clusters", handler.Wrap(http.HandlerFunc(clusterHandler.CreateCluster), nsMW, authMW, configWrite))
	mux.Handle("PUT /api/v1/clusters/{name}", handler.Wrap(http.HandlerFunc(clusterHandler.UpdateCluster), nsMW, authMW, configWrite))
	mux.Handle("DELETE /api/v1/clusters/{name}", handler.Wrap(http.HandlerFunc(clusterHandler.DeleteCluster), nsMW, authMW, configWrite))
	mux.Handle("POST /api/v1/clusters/{name}/rollback/{version}", handler.Wrap(http.HandlerFunc(clusterHandler.RollbackCluster), nsMW, authMW, configWrite))

	// -- Status --
	mux.Handle("GET /api/v1/status", handler.Wrap(http.HandlerFunc(statusHandler.AggregateStatus), nsMW, authMW, statusRead))
	mux.Handle("GET /api/v1/status/instances", handler.Wrap(http.HandlerFunc(statusHandler.ListInstances), nsMW, authMW, statusRead))
	mux.Handle("GET /api/v1/status/controller", handler.Wrap(http.HandlerFunc(statusHandler.GetController), nsMW, authMW, statusRead))
	mux.Handle("PUT /api/v1/status/instances", handler.Wrap(http.HandlerFunc(statusHandler.ReportInstances), nsMW, authMW, statusWrite))
	mux.Handle("PUT /api/v1/status/controller", handler.Wrap(http.HandlerFunc(statusHandler.ReportController), nsMW, authMW, statusWrite))

	// -- Audit --
	mux.Handle("GET /api/v1/audit", handler.Wrap(http.HandlerFunc(auditHandler.ListAuditLog), nsMW, authMW, auditRead))

	// -- Grafana dashboards --
	mux.Handle("GET /api/v1/grafana/dashboards", handler.Wrap(http.HandlerFunc(grafanaHandler.ListDashboards), nsMW, authMW, configRead))
	mux.Handle("POST /api/v1/grafana/dashboards", handler.Wrap(http.HandlerFunc(grafanaHandler.PutDashboard), nsMW, authMW, configWrite))
	mux.Handle("PUT /api/v1/grafana/dashboards", handler.Wrap(http.HandlerFunc(grafanaHandler.PutDashboard), nsMW, authMW, configWrite))
	mux.Handle("DELETE /api/v1/grafana/dashboards/{id}", handler.Wrap(http.HandlerFunc(grafanaHandler.DeleteDashboard), nsMW, authMW, configWrite))

	// -- Credentials --
	mux.Handle("GET /api/v1/credentials", handler.Wrap(http.HandlerFunc(credentialHandler.ListCredentials), nsMW, authMW, credRead))
	mux.Handle("POST /api/v1/credentials", handler.Wrap(http.HandlerFunc(credentialHandler.CreateCredential), nsMW, authMW, credWrite))
	mux.Handle("PUT /api/v1/credentials/{id}", handler.Wrap(http.HandlerFunc(credentialHandler.UpdateCredential), nsMW, authMW, credWrite))
	mux.Handle("DELETE /api/v1/credentials/{id}", handler.Wrap(http.HandlerFunc(credentialHandler.DeleteCredential), nsMW, authMW, credWrite))

	// -- Members --
	mux.Handle("GET /api/v1/members", handler.Wrap(http.HandlerFunc(memberHandler.ListMembers), nsMW, authMW, memberRead))
	mux.Handle("POST /api/v1/members", handler.Wrap(http.HandlerFunc(memberHandler.AddMember), nsMW, authMW, memberWrite))
	mux.Handle("DELETE /api/v1/members/{sub}", handler.Wrap(http.HandlerFunc(memberHandler.RemoveMember), nsMW, authMW, memberWrite))

	// -- Group bindings --
	mux.Handle("GET /api/v1/group-bindings", handler.Wrap(http.HandlerFunc(memberHandler.ListGroupBindings), nsMW, authMW, memberRead))
	mux.Handle("POST /api/v1/group-bindings", handler.Wrap(http.HandlerFunc(memberHandler.SetGroupBinding), nsMW, authMW, memberWrite))
	mux.Handle("DELETE /api/v1/group-bindings/{group}", handler.Wrap(http.HandlerFunc(memberHandler.RemoveGroupBinding), nsMW, authMW, memberWrite))

	// -- Admin: global user management --
	mux.Handle("GET /api/v1/users", handler.Wrap(http.HandlerFunc(memberHandler.ListUsers), authMW, adminUsers))
	mux.Handle("POST /api/v1/users", handler.Wrap(http.HandlerFunc(memberHandler.CreateBuiltinUser), authMW, adminUsers))
	mux.Handle("PUT /api/v1/users/{sub}/admin", handler.Wrap(http.HandlerFunc(memberHandler.SetAdmin), authMW, adminUsers))
	mux.Handle("PUT /api/v1/users/{sub}", handler.Wrap(http.HandlerFunc(memberHandler.UpdateUser), authMW, adminUsers))
	mux.Handle("DELETE /api/v1/users/{sub}", handler.Wrap(http.HandlerFunc(memberHandler.DeleteUser), authMW, adminUsers))
	mux.Handle("PUT /api/v1/users/{sub}/force-password-change", handler.Wrap(http.HandlerFunc(memberHandler.ForcePasswordChange), authMW, adminUsers))
	mux.Handle("PUT /api/v1/users/{sub}/reset-password", handler.Wrap(http.HandlerFunc(memberHandler.ResetUserPassword), authMW, adminUsers))

	// -- Namespaces --
	mux.Handle("GET /api/v1/namespaces", handler.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nsList, err := pgStore.ListNamespaces(r.Context())
		if err != nil {
			handler.ErrJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		handler.JSON(w, http.StatusOK, map[string]any{"namespaces": nsList})
	}), authMW, nsRead))
	mux.Handle("POST /api/v1/namespaces", handler.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			handler.ErrJSON(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			handler.ErrJSON(w, http.StatusBadRequest, "namespace name is required")
			return
		}
		if errMsg := store.ValidateNamespaceName(req.Name); errMsg != "" {
			handler.ErrJSON(w, http.StatusBadRequest, errMsg)
			return
		}
		if err := pgStore.CreateNamespace(r.Context(), req.Name); err != nil {
			if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
				handler.ErrJSON(w, http.StatusConflict, "namespace already exists")
				return
			}
			handler.ErrJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Auto-add creator as owner of the new namespace (OIDC users only).
		if claims := handler.OIDCClaimsFromContext(r.Context()); claims != nil {
			_ = pgStore.SetNamespaceMember(r.Context(), req.Name, claims.Sub, store.RoleOwner)
		}
		handler.JSON(w, http.StatusCreated, map[string]any{"name": req.Name})
	}), authMW, nsWrite))

	// Static frontend SPA
	distDir := "./web/dist"
	if _, err := os.Stat(distDir); err == nil {
		staticFS := http.FileServer(http.Dir(distDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if _, err := fs.Stat(os.DirFS(distDir), r.URL.Path[1:]); err != nil {
				http.ServeFile(w, r, distDir+"/index.html")
				return
			}
			staticFS.ServeHTTP(w, r)
		})
	}

	// Global middleware: Recovery → CORS
	var h http.Handler = mux
	h = handler.CORS(h)
	h = handler.Recovery(sugar, h)

	srv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      h,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		sugar.Infof("hermes control plane starting on %s", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			sugar.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Stale instance/controller reaper
	// Periodically marks instances and controllers as "offline" if they haven't
	// reported within the threshold. Idempotent UPDATE — safe to run on every replica.
	go func() {
		const (
			reaperInterval           = 15 * time.Second
			instanceStaleThreshold   = 30 * time.Second // 2x gateway lease TTL (15s)
			controllerStaleThreshold = 30 * time.Second // 3x controller heartbeat (10s)
		)
		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if stale, err := pgStore.MarkStaleInstances(ctx, instanceStaleThreshold); err != nil {
					sugar.Warnf("stale instance reaper: %v", err)
				} else {
					for _, e := range stale {
						sugar.Warnf("gateway instance offline: ns=%s id=%s", e.Namespace, e.ID)
					}
				}
				if stale, err := pgStore.MarkStaleControllers(ctx, controllerStaleThreshold); err != nil {
					sugar.Warnf("stale controller reaper: %v", err)
				} else {
					for _, e := range stale {
						sugar.Warnf("controller offline: ns=%s id=%s", e.Namespace, e.ID)
					}
				}
				cancel()
			}
		}
	}()

	<-quit

	sugar.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
