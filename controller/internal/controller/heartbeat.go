package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// controllerReport is the payload sent to controlplane PUT /api/v1/status/controller.
type controllerReport struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	ConfigRevision  int64  `json:"config_revision"`
}

// heartbeatLoop periodically reports controller's own status to controlplane.
func (c *Controller) heartbeatLoop(ctx context.Context) {
	if err := c.reportControllerStatus(ctx, "running"); err != nil {
		c.logger.Warnf("initial controller heartbeat failed: %v", err)
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = c.reportControllerStatus(shutCtx, "shutting_down")
			cancel()
			return
		case <-ticker.C:
			if err := c.reportControllerStatus(ctx, "running"); err != nil {
				c.logger.Warnf("controller heartbeat failed: %v", err)
			}
		}
	}
}

func (c *Controller) reportControllerStatus(ctx context.Context, status string) error {
	report := controllerReport{
		ID:              c.hostname,
		Status:          status,
		StartedAt:       c.startedAt.Format(time.RFC3339),
		LastHeartbeatAt: time.Now().Format(time.RFC3339),
		ConfigRevision:  c.GetRevision(),
	}

	body, _ := json.Marshal(report)
	url := c.cfg.ControlPlane.URL + "/api/v1/status/controller"
	req, err := http.NewRequestWithContext(ctx, "PUT", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("report controller status: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("controlplane returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
