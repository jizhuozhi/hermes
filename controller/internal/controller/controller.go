package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jizhuozhi/hermes/controller/internal/config"
	"github.com/jizhuozhi/hermes/controller/internal/transport"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// WatchResponse is the response from the controlplane watch API.
type WatchResponse struct {
	Events   []ChangeEvent `json:"events"`
	Revision int64         `json:"revision"`
	Total    int           `json:"total"`
}

// ChangeEvent represents a single configuration change from the controlplane.
type ChangeEvent struct {
	Revision int64           `json:"revision"`
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Action   string          `json:"action"`
	Domain   json.RawMessage `json:"domain,omitempty"`
	Cluster  json.RawMessage `json:"cluster,omitempty"`
}

// Controller watches the controlplane for config changes and syncs them to etcd.
type Controller struct {
	cfg         *config.Config
	etcdClient  *clientv3.Client
	httpClient  *http.Client
	logger      *zap.SugaredLogger
	revision    atomic.Int64
	isLeader    atomic.Bool
	startedAt   time.Time
	hostname    string
	reconcileCh chan reconcileReq
}

// reconcileReq is sent from reconcileLoop to the main loop.
type reconcileReq struct {
	done chan error
}

// New creates a new Controller.
func New(cfg *config.Config, logger *zap.SugaredLogger) (*Controller, error) {
	etcdCfg := clientv3.Config{
		Endpoints:   cfg.Etcd.Endpoints,
		DialTimeout: 5 * time.Second,
	}
	if cfg.Etcd.Username != "" {
		etcdCfg.Username = cfg.Etcd.Username
		etcdCfg.Password = cfg.Etcd.Password
	}

	client, err := clientv3.New(etcdCfg)
	if err != nil {
		return nil, fmt.Errorf("etcd connect: %w", err)
	}

	hostname, _ := os.Hostname()

	region := cfg.ControlPlane.Region // already defaulted in config.Load

	var rt http.RoundTripper = http.DefaultTransport
	if cfg.Auth.AccessKey != "" && cfg.Auth.SecretKey != "" {
		rt = &transport.HMACSigning{
			AK:     cfg.Auth.AccessKey,
			SK:     cfg.Auth.SecretKey,
			Region: region,
			Base:   http.DefaultTransport,
		}
		logger.Infof("HMAC-SHA256 authentication enabled (ak=%s, region=%s)", cfg.Auth.AccessKey, region)
	} else if region != "default" {
		rt = &transport.RegionOnly{Region: region, Base: http.DefaultTransport}
		logger.Infof("region=%s (no auth)", region)
	}

	ctrl := &Controller{
		cfg:         cfg,
		etcdClient:  client,
		httpClient:  &http.Client{Timeout: 60 * time.Second, Transport: rt},
		logger:      logger,
		startedAt:   time.Now(),
		hostname:    hostname,
		reconcileCh: make(chan reconcileReq),
	}
	return ctrl, nil
}

// GetRevision returns the current revision (safe for concurrent use).
func (c *Controller) GetRevision() int64 {
	return c.revision.Load()
}

// SetRevision updates the current revision (safe for concurrent use).
func (c *Controller) SetRevision(rev int64) {
	c.revision.Store(rev)
}

// Close releases resources.
func (c *Controller) Close() {
	c.etcdClient.Close()
}

func (c *Controller) IsLeader() bool {
	return c.isLeader.Load()
}

func (c *Controller) SetLeader(v bool) {
	c.isLeader.Store(v)
}

func (c *Controller) EtcdClient() *clientv3.Client {
	return c.etcdClient
}

func (c *Controller) Hostname() string {
	return c.hostname
}

// Run starts the main controller loop: initial reconcile, then short-poll for changes.
func (c *Controller) Run(ctx context.Context) error {
	if err := c.Reconcile(ctx); err != nil {
		return fmt.Errorf("initial reconcile: %w", err)
	}

	rev, err := c.fetchRevision(ctx)
	if err != nil {
		c.logger.Warnf("failed to fetch initial revision: %v", err)
	} else {
		c.SetRevision(rev)
	}

	c.publishRevisionToEtcd(ctx)
	c.logger.Infof("controller started, initial revision=%d", c.GetRevision())

	reconcileInterval := time.Duration(c.cfg.ControlPlane.ReconcileInterval) * time.Second
	if reconcileInterval <= 0 {
		reconcileInterval = 60 * time.Second
	}
	go c.reconcileLoop(ctx, reconcileInterval)
	go c.watchInstances(ctx)
	go c.heartbeatLoop(ctx)

	pollInterval := time.Duration(c.cfg.ControlPlane.PollInterval) * time.Second
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("controller stopping")
			return nil
		case rr := <-c.reconcileCh:
			rr.done <- c.Reconcile(ctx)
		case <-ticker.C:
			c.pollOnce(ctx)
		}
	}
}

// pollOnce does a single short-poll to the controlplane and applies any events.
func (c *Controller) pollOnce(ctx context.Context) {
	events, newRev, err := c.fetchChanges(ctx)
	if err != nil {
		c.logger.Warnf("poll error: %v", err)
		return
	}

	for _, ev := range events {
		if err := c.applyEvent(ctx, ev); err != nil {
			c.logger.Errorf("apply event error: %v", err)
		}
	}

	if newRev > c.GetRevision() {
		c.SetRevision(newRev)
		c.publishRevisionToEtcd(ctx)
	}
}

func (c *Controller) fetchRevision(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.ControlPlane.URL+"/api/v1/config/revision", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("controlplane returned %d for revision", resp.StatusCode)
	}

	var result struct {
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.Revision, nil
}

// fetchChanges does a short-poll GET /api/v1/config/watch?revision=N.
// The server returns immediately with any changes since revision N.
func (c *Controller) fetchChanges(ctx context.Context) ([]ChangeEvent, int64, error) {
	url := fmt.Sprintf("%s/api/v1/config/watch?revision=%d", c.cfg.ControlPlane.URL, c.GetRevision())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, c.GetRevision(), err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.GetRevision(), err
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, c.GetRevision(), fmt.Errorf("controlplane returned %d for watch", resp.StatusCode)
	}

	var wr WatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, c.GetRevision(), fmt.Errorf("decode watch response: %w", err)
	}

	return wr.Events, wr.Revision, nil
}

func (c *Controller) applyEvent(ctx context.Context, ev ChangeEvent) error {
	var prefix string
	switch ev.Kind {
	case "domain":
		prefix = c.cfg.Etcd.DomainPrefix
	case "cluster":
		prefix = c.cfg.Etcd.ClusterPrefix
	default:
		c.logger.Warnf("unknown event kind %q, skipping", ev.Kind)
		return nil
	}
	prefix = strings.TrimRight(prefix, "/")
	key := prefix + "/" + ev.Name

	switch ev.Action {
	case "delete":
		_, err := c.etcdClient.Delete(ctx, key)
		if err != nil {
			return fmt.Errorf("etcd delete %s: %w", key, err)
		}
		c.logger.Infof("applied delete: %s", key)

	default: // create, update, rollback, import
		var data json.RawMessage
		switch ev.Kind {
		case "domain":
			data = ev.Domain
		case "cluster":
			data = ev.Cluster
		}
		if data == nil {
			c.logger.Warnf("skip event with nil data: kind=%s name=%s action=%s", ev.Kind, ev.Name, ev.Action)
			return nil
		}
		_, err := c.etcdClient.Put(ctx, key, string(data))
		if err != nil {
			return fmt.Errorf("etcd put %s: %w", key, err)
		}
		c.logger.Infof("applied %s: %s", ev.Action, key)
	}
	return nil
}

// publishRevisionToEtcd writes the controlplane config revision to etcd
// so gateways can read the business-meaningful version number.
func (c *Controller) publishRevisionToEtcd(ctx context.Context) {
	metaPrefix := strings.TrimRight(c.cfg.Etcd.MetaPrefix, "/")
	if metaPrefix == "" {
		metaPrefix = "/hermes/meta"
	}
	key := metaPrefix + "/config_revision"
	val := strconv.FormatInt(c.GetRevision(), 10)
	if _, err := c.etcdClient.Put(ctx, key, val); err != nil {
		c.logger.Warnf("failed to publish config revision to etcd: %v", err)
	} else {
		c.logger.Infof("published config_revision=%d to etcd key=%s", c.GetRevision(), key)
	}
}
