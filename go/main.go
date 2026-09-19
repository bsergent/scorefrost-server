package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/lib/pq"
)

func main() {
	if os.Getenv("DOCKER_ENV") != "true" {
		err := godotenv.Load()
		if err != nil {
			log.Printf("Warning: Could not load .env file, relying on environment variables")
		}
	}

	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "8080"
	}

	db, err := connectToDB()
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()

	if err := verifySchemaCompatibility(db, "./sql/migrations"); err != nil {
		log.Fatalf("Database schema check failed: %v", err)
	}

	// Sync dev user's API key hash from environment variable.
	// Schema changes are migration-only and must be applied before API startup.
	if err := syncDevAPIKeyHash(db); err != nil {
		log.Fatalf("Failed to sync dev user API key: %v", err)
	}

	// Create rate limiter: 20 requests per minute per IP
	// Initialize rate limiter
	rateLimitStr := os.Getenv("API_MAX_REQUESTS_PER_MINUTE")
	if rateLimitStr == "" {
		rateLimitStr = "20"
	}
	rateLimitInt, err := strconv.Atoi(rateLimitStr)
	if err != nil {
		log.Fatalf("Invalid API_MAX_REQUESTS_PER_MINUTE value: %v", err)
	}
	rateLimiter := NewRateLimiter(rateLimitInt, 1*time.Minute)
	log.Printf("Rate limiter initialized: %d requests/minute per IP", rateLimitInt)

	// Set up routes using shared function
	mux := setupRoutes(db)

	// Wrap mux with middleware
	loggingHandler := requestLogMiddleware(mux)
	corsHandler := corsMiddleware(loggingHandler)
	handler := RateLimitMiddleware(rateLimiter, corsHandler)

	// Create HTTP server
	server := &http.Server{
		Addr:    ":" + apiPort,
		Handler: handler,
	}

	// Channel to listen for interrupt signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		log.Printf("Starting server on :%s", apiPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Wait for interrupt signal
	<-stop
	log.Println("Shutting down server...")

	// Create a context with timeout for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Attempt graceful shutdown
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server gracefully stopped")
}

func connectToDB() (*sql.DB, error) {
	dbHost := os.Getenv("PGHOST")
	dbPort := os.Getenv("PGPORT")
	dbUser := os.Getenv("PGUSER")
	dbPassword := os.Getenv("PGPASSWORD")
	dbName := os.Getenv("PGDB")

	// PostgreSQL DSN
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPassword, dbName,
	)
	// log.Printf("Connection String: %s", dsn)

	// Build a connector whose Connect method registers a pq NoticeHandler on
	// every individual connection in the pool, so PostgreSQL WARNING messages
	// (e.g. unknown score types skipped during submission) reach the Go logger.
	baseConnector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create pq connector: %w", err)
	}
	connector := &noticeConnector{
		Connector: baseConnector,
		handler: func(notice *pq.Error) {
			log.Printf("[PostgreSQL %s] %s", notice.Severity, notice.Message)
		},
	}

	var db *sql.DB

	// Try to connect with retries
	const maxRetries = 5
	for range maxRetries {
		db = sql.OpenDB(connector)
		err = db.Ping()
		if err == nil {
			break
		}
		db.Close()
		time.Sleep(2 * time.Second)
	}

	// Return any connection errors
	if err != nil {
		return nil, err
	}

	log.Println("Connected to PostgreSQL!")
	return db, nil
}

// noticeConnector wraps a pq.Connector and registers a pq.NoticeHandler on
// every new driver.Conn so that PostgreSQL WARNING/NOTICE messages surface in
// the application log.
type noticeConnector struct {
	*pq.Connector
	handler func(*pq.Error)
}

func (nc *noticeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := nc.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pq.SetNoticeHandler(conn, nc.handler)
	return conn, nil
}

// requestLogMiddleware logs the HTTP method and URL of each request
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","time":"%s"}`, time.Now().UTC().Format(time.RFC3339))
}
