// Command templ-app serves the merchant dashboard and runs the background
// workers. Configuration comes from the environment (see .env.example and
// internal/app.Config). Without DATABASE_URL it still serves the fixture
// pages so the UI can be developed on its own.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"templ-app/internal/app"
	"templ-app/internal/web"
	"templ-app/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := app.ConfigFromEnv()
	level := slog.LevelInfo
	if !cfg.Production() {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	var a *app.App
	if cfg.DatabaseURL == "" {
		log.Warn("DATABASE_URL not set: serving fixture pages only, no workers")
	} else {
		var err error
		if a, err = app.New(ctx, cfg, log); err != nil {
			return err
		}
		defer a.Close()
	}

	ln, err := listen()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           web.New(a).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		fmt.Printf("Server is running on http://localhost:%d\n", ln.Addr().(*net.TCPAddr).Port)
		errc <- srv.Serve(ln)
	}()
	if a != nil {
		go worker.Run(ctx, a, worker.Intervals{})
	}

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// listen binds the port. An explicit PORT binds exactly (the templ proxy in
// the Taskfile points at it, so silently moving would break hot reload);
// without PORT it walks from 8090 to the next free port like node dev
// servers do.
func listen() (net.Listener, error) {
	if env := os.Getenv("PORT"); env != "" {
		port, err := strconv.Atoi(env)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q", env)
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return nil, fmt.Errorf("port %d is busy - stop the other app or run with another port, e.g. 'task dev PORT=%d'", port, port+1)
		}
		return ln, nil
	}
	for port := 8090; port < 8100; port++ {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("no free port found from 8090 upwards")
}
