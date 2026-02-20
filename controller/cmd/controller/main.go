package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jizhuozhi/hermes/controller/internal/config"
	"github.com/jizhuozhi/hermes/controller/internal/controller"

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

	ctrl, err := controller.New(cfg, sugar)
	if err != nil {
		log.Fatalf("failed to create controller: %v", err)
	}
	defer ctrl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register signal handler in the main goroutine BEFORE starting Run
	// so no signal can be lost between program start and Notify registration.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		sugar.Info("received shutdown signal")
		cancel()
	}()

	if cfg.Election.Enabled {
		sugar.Info("leader election enabled, entering campaign loop")
		if err := ctrl.RunWithElection(ctx); err != nil {
			sugar.Fatalf("controller error: %v", err)
		}
	} else {
		ctrl.SetLeader(true)
		if err := ctrl.Run(ctx); err != nil {
			sugar.Fatalf("controller error: %v", err)
		}
	}
}
