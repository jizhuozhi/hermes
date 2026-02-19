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

// instanceReport is the payload sent to controlplane PUT /api/v1/status/instances.
type instanceReport struct {
	Instances []instanceInfo `json:"instances"`
}

type instanceInfo struct {
	ID              string `json:"id"`
	Status          string `json:"status,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	RegisteredAt    string `json:"registered_at,omitempty"`
	LastKeepaliveAt string `json:"last_keepalive_at,omitempty"`
	ConfigRevision  int64  `json:"config_revision,omitempty"`
}

// watchInstances watches etcd /hermes/instances/ for gateway self-registration
// changes and reports the current instance list to the control plane.
func (c *Controller) watchInstances(ctx context.Context) {
	prefix := strings.TrimRight(c.cfg.Etcd.InstancePrefix, "/") + "/"

	if err := c.reportInstances(ctx, prefix); err != nil {
		c.logger.Warnf("initial instance report failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		watchCh := c.etcdClient.Watch(ctx, prefix, clientv3.WithPrefix())
		for resp := range watchCh {
			if resp.Err() != nil {
				c.logger.Warnf("instance watch error: %v", resp.Err())
				break
			}
			if err := c.reportInstances(ctx, prefix); err != nil {
				c.logger.Warnf("instance report failed: %v", err)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
			c.logger.Info("instance watch reconnecting...")
		}
	}
}

func (c *Controller) reportInstances(ctx context.Context, prefix string) error {
	resp, err := c.etcdClient.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}

	instances := make([]instanceInfo, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var info instanceInfo
		if err := json.Unmarshal(kv.Value, &info); err != nil {
			info.ID = strings.TrimPrefix(string(kv.Key), prefix)
		}
		if info.ID == "" {
			info.ID = strings.TrimPrefix(string(kv.Key), prefix)
		}
		instances = append(instances, info)
	}

	report := instanceReport{Instances: instances}
	body, _ := json.Marshal(report)

	url := c.cfg.ControlPlane.URL + "/api/v1/status/instances"
	req, err := http.NewRequestWithContext(ctx, "PUT", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("report to controlplane: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, httpResp.Body)
		httpResp.Body.Close()
	}()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("controlplane returned %d: %s", httpResp.StatusCode, string(respBody))
	}

	c.logger.Infof("reported %d gateway instances to controlplane", len(instances))
	return nil
}
