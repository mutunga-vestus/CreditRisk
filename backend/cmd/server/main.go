package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"loanscore/backend/internal/handlers"
	"loanscore/backend/internal/mailer"
	"loanscore/backend/internal/ml"
	"loanscore/backend/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	loadDotEnv(".env")
	ctx := context.Background()
	st, err := store.New(ctx, env("DATABASE_URL", "postgres://postgres:festus5531@localhost:5432/CreditRisk?sslmode=disable"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	srv := handlers.New(handlers.Deps{
		Store: st,
		Mail: &mailer.Mailer{
			Host: os.Getenv("SMTP_HOST"), Port: env("SMTP_PORT", "587"),
			User: os.Getenv("SMTP_USER"), Pass: os.Getenv("SMTP_PASS"),
			From: env("SMTP_FROM", "no-reply@example.com"),
		},
		ML:           ml.New(env("ML_API_URL", "http://localhost:8100"), os.Getenv("ML_API_KEY")),
		BaseURL:      env("BASE_URL", "http://localhost:8080"),
		SecureCookie: env("COOKIE_SECURE", "false") == "true",
	})
	hs := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("listening on %s", hs.Addr)
	log.Fatal(hs.ListenAndServe())
}
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
