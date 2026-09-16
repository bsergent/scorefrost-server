package main

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"strings"
)

// getIPAddress extracts the IP address from a request
// Handles X-Forwarded-For for proxies and strips port from RemoteAddr
func getIPAddress(r *http.Request) string {
	// Check proxy headers first
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if idx := strings.Index(ip, ","); idx != -1 {
			ip = ip[:idx]
		}
		return strings.TrimSpace(ip)
	}

	ip = r.Header.Get("X-Real-IP")
	if ip != "" {
		return ip
	}

	// Fall back to RemoteAddr
	ip = r.RemoteAddr
	// Strip port from RemoteAddr (format is "IP:port")
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return host
	}

	return ip
}

// Context key types to avoid collisions
type contextKey string

const (
	contextKeyUserID      contextKey = "user_id"
	contextKeyDisplayName contextKey = "display_name"
	contextKeyFriendCode  contextKey = "friend_code"
)

// authMiddleware wraps a handler to require API key authentication
// Only applies to endpoints where you explicitly use it
func authMiddleware(db *sql.DB, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")

		// Check format: "Bearer <api_key>"
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "Unauthorized: Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		apiKey := strings.TrimPrefix(auth, "Bearer ")
		apiKey = strings.TrimSpace(apiKey)

		if apiKey == "" {
			http.Error(w, "Unauthorized: API key is required", http.StatusUnauthorized)
			return
		}

		// Hash the provided API key
		hashedKey := hashAPIKey(apiKey)

		// Look up user by API key hash
		var userID UserID
		var displayName DisplayName
		var friendCode FriendCode
		err := db.QueryRow(`
			SELECT id, COALESCE(display_name, ''), friend_code
			FROM "user"
			WHERE api_key_hash = $1
		`, hashedKey).Scan(&userID, &displayName, &friendCode)

		if err == sql.ErrNoRows {
			log.Printf("Authentication failed: Invalid API key from IP %s", getIPAddress(r))
			http.Error(w, "Unauthorized: Invalid API key", http.StatusUnauthorized)
			return
		}

		if err != nil {
			log.Printf("Authentication error: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// log.Printf("Authenticated user: %s (%s)", displayName, friendCode)

		// Store user info in request context for handler to use
		ctx := context.WithValue(r.Context(), contextKeyUserID, userID)
		ctx = context.WithValue(ctx, contextKeyDisplayName, displayName)
		ctx = context.WithValue(ctx, contextKeyFriendCode, friendCode)

		// Call the protected handler
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// Helper functions to extract user info from request context

// GetUserID extracts the authenticated user's ID from the request context
func GetUserID(r *http.Request) (UserID, bool) {
	userID, ok := r.Context().Value(contextKeyUserID).(UserID)
	return userID, ok
}

// GetDisplayName extracts the authenticated user's display name from the request context
func GetDisplayName(r *http.Request) (DisplayName, bool) {
	displayName, ok := r.Context().Value(contextKeyDisplayName).(DisplayName)
	return displayName, ok
}

// GetFriendCode extracts the authenticated user's friend code from the request context
func GetFriendCode(r *http.Request) (FriendCode, bool) {
	friendCode, ok := r.Context().Value(contextKeyFriendCode).(FriendCode)
	return friendCode, ok
}
