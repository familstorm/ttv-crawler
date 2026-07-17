package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/familstorm/crawler-truyen-ttv/internal/config"
	"github.com/familstorm/crawler-truyen-ttv/internal/crawler"
	"github.com/familstorm/crawler-truyen-ttv/internal/fetcher"
	"github.com/familstorm/crawler-truyen-ttv/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Lỗi:", err)
		os.Exit(1)
	}
}

func run() error {
	command := "run"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "help" || command == "-h" || command == "--help" {
		usage()
		return nil
	}
	if err := config.LoadDotEnv(".env"); err != nil {
		return fmt.Errorf("đọc .env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}

	f, err := fetcher.New(ctx, fetcher.Config{
		Interval:          cfg.RequestInterval,
		Jitter:            cfg.RequestJitter,
		Timeout:           cfg.HTTPTimeout,
		Retries:           cfg.HTTPRetries,
		MaxResponseBytes:  cfg.MaxResponseBytes,
		UserAgent:         cfg.UserAgent,
		RobotsFailOpen:    cfg.RobotsFailOpen,
		BrowserExecutable: cfg.BrowserExecutable,
	}, logger)
	if err != nil {
		return err
	}
	defer f.Close()
	runner := crawler.New(db, f, crawler.Config{
		Workers:        cfg.Workers,
		MaxJobAttempts: cfg.MaxJobAttempts,
		CatalogMaxPage: cfg.CatalogMaxPage,
		IdleExitAfter:  cfg.IdleExitAfter,
	}, logger)

	switch command {
	case "migrate":
		logger.Info("migration đã cập nhật")
		return nil
	case "seed":
		if err := runner.Seed(ctx, cfg.StartURL); err != nil {
			return err
		}
		logger.Info("đã thêm URL khởi đầu", "url", cfg.StartURL)
		return nil
	case "run":
		if err := runner.Seed(ctx, cfg.StartURL); err != nil {
			return err
		}
		err := runner.Run(ctx)
		if errors.Is(err, context.Canceled) {
			logger.Info("crawler đã dừng an toàn; lần chạy sau sẽ tiếp tục queue")
			return nil
		}
		return err
	case "status":
		stats, err := db.Stats(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Queue: pending=%d processing=%d completed=%d failed=%d\n", stats.Pending, stats.Processing, stats.Completed, stats.Failed)
		fmt.Printf("Dữ liệu: stories=%d chapters=%d\n", stats.Stories, stats.Chapters)
		return nil
	case "retry-failed":
		count, err := db.RetryFailed(ctx)
		if err != nil {
			return err
		}
		logger.Info("đã đưa job lỗi về queue", "count", count)
		return nil
	default:
		usage()
		return fmt.Errorf("lệnh không hỗ trợ: %s", command)
	}
}

func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func usage() {
	fmt.Print(`TTV personal archiver

Usage:
  ttv-crawler migrate       tạo/cập nhật schema PostgreSQL
  ttv-crawler seed          thêm START_URL vào queue
  ttv-crawler run           seed và chạy worker (mặc định)
  ttv-crawler status        xem tiến độ
  ttv-crawler retry-failed  thử lại các job đã hết số lần retry
`)
}
