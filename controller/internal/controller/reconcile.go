package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// reconcileLoop periodically requests reconcile via the main loop to avoid
// concurrent etcd writes.
func (c *Controller) reconcileLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rr := reconcileReq{done: make(chan error, 1)}
			select {
			case c.reconcileCh <- rr:
				if err := <-rr.done; err != nil {
					c.logger.Errorf("periodic reconcile failed: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

// Reconcile fetches the full desired state from CP, compares it to what's
// currently in etcd, and applies the minimal diff (put missing/stale, delete unknown).
func (c *Controller) Reconcile(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.ControlPlane.URL+"/api/v1/config", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch config: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("controlplane returned %d for full config", resp.StatusCode)
	}

	var result struct {
		Config struct {
			Domains  []json.RawMessage `json:"domains"`
			Clusters []json.RawMessage `json:"clusters"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	domainPrefix := strings.TrimRight(c.cfg.Etcd.DomainPrefix, "/")
	clusterPrefix := strings.TrimRight(c.cfg.Etcd.ClusterPrefix, "/")

	desiredDomains := make(map[string]string, len(result.Config.Domains))
	for _, raw := range result.Config.Domains {
		name := extractName(raw)
		if name != "" {
			desiredDomains[domainPrefix+"/"+name] = canonicalJSON(raw)
		}
	}
	desiredClusters := make(map[string]string, len(result.Config.Clusters))
	for _, raw := range result.Config.Clusters {
		name := extractName(raw)
		if name != "" {
			desiredClusters[clusterPrefix+"/"+name] = canonicalJSON(raw)
		}
	}

	actualDomains, err := c.etcdClient.Get(ctx, domainPrefix+"/", clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("list etcd domains: %w", err)
	}
	actualClusters, err := c.etcdClient.Get(ctx, clusterPrefix+"/", clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("list etcd clusters: %w", err)
	}

	var puts, deletes int

	diffOps := c.diffKeys(desiredDomains, actualDomains)
	diffOps = append(diffOps, c.diffKeys(desiredClusters, actualClusters)...)

	for _, op := range diffOps {
		switch op.opType {
		case "put":
			if _, err := c.etcdClient.Put(ctx, op.key, op.value); err != nil {
				c.logger.Errorf("reconcile put %s: %v", op.key, err)
				continue
			}
			puts++
		case "delete":
			if _, err := c.etcdClient.Delete(ctx, op.key); err != nil {
				c.logger.Errorf("reconcile delete %s: %v", op.key, err)
				continue
			}
			deletes++
		}
	}

	if puts > 0 || deletes > 0 {
		c.logger.Infof("reconcile done: puts=%d, deletes=%d (domains_desired=%d, clusters_desired=%d)",
			puts, deletes, len(desiredDomains), len(desiredClusters))
	} else {
		c.logger.Debugf("reconcile done: etcd is clean (domains=%d, clusters=%d)",
			len(desiredDomains), len(desiredClusters))
	}
	return nil
}

type diffOp struct {
	opType string // "put" or "delete"
	key    string
	value  string
}

func (c *Controller) diffKeys(desired map[string]string, actual *clientv3.GetResponse) []diffOp {
	var ops []diffOp

	actualMap := make(map[string]string, len(actual.Kvs))
	for _, kv := range actual.Kvs {
		actualMap[string(kv.Key)] = string(kv.Value)
	}

	for key, desiredVal := range desired {
		actualVal, exists := actualMap[key]
		if !exists {
			c.logger.Warnf("reconcile: missing key %s, will put", key)
			ops = append(ops, diffOp{opType: "put", key: key, value: desiredVal})
		} else if canonicalJSON(json.RawMessage(actualVal)) != desiredVal {
			c.logger.Warnf("reconcile: stale key %s, will update", key)
			ops = append(ops, diffOp{opType: "put", key: key, value: desiredVal})
		}
	}

	for key := range actualMap {
		if _, exists := desired[key]; !exists {
			c.logger.Warnf("reconcile: dirty key %s, will delete", key)
			ops = append(ops, diffOp{opType: "delete", key: key})
		}
	}

	return ops
}

func extractName(raw json.RawMessage) string {
	var h struct {
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &h)
	return h.Name
}

func canonicalJSON(raw json.RawMessage) string {
	var obj interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
