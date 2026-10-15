package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aqutw/jobLineBot/internal/bot"
	"github.com/aqutw/jobLineBot/internal/job104"
	"github.com/aqutw/jobLineBot/internal/line"
	"github.com/aqutw/jobLineBot/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fetcher, err := job104.New()
	if err != nil {
		logger.Error("fetcher initialization failed")
		os.Exit(1)
	}
	model := bot.NewModel(os.Getenv("OPENROUTER_API_KEY"), os.Getenv("OPENROUTER_MODEL"))
	lineClient := line.New(os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"))
	server := web.New(ctx, &bot.Service{Fetcher: fetcher, Model: model}, lineClient, web.Config{
		Secret: os.Getenv("LINE_CHANNEL_SECRET"), LINEEnabled: os.Getenv("LINE_CHANNEL_SECRET") != "" && lineClient.Token != "", ModelEnabled: model.Key != "", APIToken: os.Getenv("API_TOKEN")}, logger)
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}
	httpServer := &http.Server{Addr: ":" + port, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 155 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	logger.Info("jobLineBot listening", "port", port, "model", model.Model)
	if err = httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server failed")
		stop()
	}
	stop()
	server.Wait()
}
