package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cleanarr/internal/cleaner"
	"cleanarr/internal/config"
	"cleanarr/internal/server"
	"cleanarr/web"
)

var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("CLEANARR_LISTEN", ":9797"), "address to listen on")
	dataDir := flag.String("config", env("CLEANARR_CONFIG", "./config"), "directory for config.json and job history")
	flag.Parse()

	cfg, err := config.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	svc := cleaner.New(cfg, *dataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go svc.Run(ctx)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(cfg, svc, web.FS, version).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("cleanarr %s listening on %s (config in %s)", version, *listen, *dataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
