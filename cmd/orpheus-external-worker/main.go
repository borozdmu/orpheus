package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus/internal/config"
	"github.com/orpheus-agents/orpheus/internal/store"
)

const heartbeat = "/tmp/cx-worker-ready"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "health" {
		s, err := os.Stat(heartbeat)
		if err != nil || time.Since(s.ModTime()) > 45*time.Second {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("External worker stopped", "type", fmt.Sprintf("%T", err))
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	w, err := readWorkflow(os.Getenv("CX_WORKFLOW_FILE"))
	if err != nil {
		return err
	}
	url := os.Getenv("DATABASE_URL")
	socket := os.Getenv("CX_EXECUTION_SOCKET")
	if url == "" || socket == "" {
		return errors.New("missing external worker configuration")
	}
	cfg, err := config.DatabaseConnectionConfig(url)
	if err != nil {
		return err
	}
	owner, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer owner.Close(context.Background())
	ok, err := store.TryWorkerLock(ctx, owner)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("another worker owns the database")
	}
	poolCfg, err := config.DatabasePoolConfig(url)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := &store.Store{Pool: pool, Settings: config.DefaultSettings()}
	executor := queue{s: s, workflow: w, backend: unixBackend(socket)}
	for ctx.Err() == nil {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := owner.Ping(probe)
		cancel()
		if err != nil {
			return err
		}
		ids, err := s.Reserved(ctx)
		if err != nil {
			return err
		}
		// Each request is a short local state transition or poll; the gateway
		// runs the paid jobs independently and owns their concurrency limits.
		var workers sync.WaitGroup
		limit := make(chan struct{}, min(w.Capacity, 12))
		for _, id := range ids {
			limit <- struct{}{}
			workers.Go(func() {
				defer func() { <-limit }()
				if err := executor.tick(ctx, id); err != nil && ctx.Err() == nil {
					slog.Warn("Will reconcile the same execution", "session", id, "type", fmt.Sprintf("%T", err))
				}
			})
		}
		workers.Wait()
		// Probe the private listener without reading payloads or creating work.
		connection, socketErr := net.DialTimeout("unix", socket, time.Second)
		if socketErr == nil {
			connection.Close()
			if err := os.WriteFile(heartbeat, nil, 0600); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}
