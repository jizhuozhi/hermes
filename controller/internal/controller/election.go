package controller

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// RunWithElection participates in leader election via etcd. Only the leader
// executes the main Run loop. When leadership is lost the Run context is
// cancelled, and the instance re-campaigns.
func (c *Controller) RunWithElection(ctx context.Context) error {
	ttl := c.cfg.Election.LeaseTTL
	if ttl <= 0 {
		ttl = 15
	}
	prefix := c.cfg.Election.Prefix
	if prefix == "" {
		prefix = "/hermes/election"
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		if err := c.campaignAndRun(ctx, prefix, ttl); err != nil {
			c.logger.Errorf("election cycle error: %v", err)
		}

		c.SetLeader(false)
		c.logger.Info("lost leadership, re-campaigning in 3s...")

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *Controller) campaignAndRun(ctx context.Context, prefix string, ttl int) error {
	session, err := concurrency.NewSession(c.etcdClient, concurrency.WithTTL(ttl))
	if err != nil {
		return fmt.Errorf("create election session: %w", err)
	}
	defer session.Close()

	election := concurrency.NewElection(session, prefix)

	c.logger.Infof("campaigning for leadership (prefix=%s, ttl=%ds)...", prefix, ttl)
	if err := election.Campaign(ctx, c.hostname); err != nil {
		return fmt.Errorf("campaign: %w", err)
	}

	c.SetLeader(true)
	c.logger.Infof("elected as leader (id=%s)", c.hostname)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Watch for session expiry (leadership loss).
	go func() {
		select {
		case <-session.Done():
			c.logger.Warn("etcd session expired, resigning leadership")
			runCancel()
		case <-runCtx.Done():
		}
	}()

	err = c.Run(runCtx)

	// Best-effort resign so a new leader can be elected immediately.
	resignCtx, resignCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer resignCancel()
	if resignErr := election.Resign(resignCtx); resignErr != nil {
		c.logger.Warnf("failed to resign leadership: %v", resignErr)
	}

	return err
}

// ObserveLeader watches the election prefix and returns a channel that emits
// the current leader's value whenever leadership changes. Useful for follower
// status reporting.
func ObserveLeader(ctx context.Context, client *clientv3.Client, prefix string, ttl int) <-chan string {
	ch := make(chan string, 1)
	go func() {
		defer close(ch)
		for {
			if ctx.Err() != nil {
				return
			}
			session, err := concurrency.NewSession(client, concurrency.WithTTL(ttl))
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
					continue
				}
			}
			election := concurrency.NewElection(session, prefix)
			observe := election.Observe(ctx)
			for resp := range observe {
				if len(resp.Kvs) > 0 {
					select {
					case ch <- string(resp.Kvs[0].Value):
					default:
					}
				}
			}
			session.Close()
		}
	}()
	return ch
}
