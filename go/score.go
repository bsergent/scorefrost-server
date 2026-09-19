package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// ScoreSubmissionRequest represents the JSON body for score submission
type ScoreSubmissionRequest struct {
	LevelID      string         `json:"level_id"`
	LevelVersion int            `json:"level_version"`
	GameVersion  string         `json:"game_version"`
	Solution     string         `json:"solution"`      // base64 encoded solution bytes
	SolutionHash string         `json:"solution_hash"` // salted hash of solution
	Scores       map[string]int `json:"scores"`        // score type -> score value
}

// BestScoreEntry represents a single best score entry
type BestScoreEntry struct {
	LevelID      string `json:"level_id"`
	LevelVersion int    `json:"level_version"`
	ScoreType    string `json:"score_type"`
	BestScore    int    `json:"best_score"`
	DisplayName  string `json:"display_name"`
	FriendCode   string `json:"friend_code"`
}

// LeaderboardEntry represents a single leaderboard entry with ranking
type LeaderboardEntry struct {
	BestScoreEntry
	Rank int `json:"rank"`
}

// Solution represents a submitted solution response
type Solution struct {
	ApiResponse
	SolutionID string `json:"solution_id"`
}

// BestScoresResponse represents the JSON response for best scores
type BestScoresResponse struct {
	ApiResponse
	Scores []BestScoreEntry `json:"scores"`
	Count  int              `json:"count"`
	Scope  string           `json:"scope"`
}

// LeaderboardResponse represents the JSON response for leaderboard
type LeaderboardResponse struct {
	ApiResponse
	Scores     []LeaderboardEntry `json:"scores"`
	Count      int                `json:"count"`
	Scope      string             `json:"scope"`
	Pagination PaginationInfo     `json:"pagination"`
}

// parseLevelsParameter parses the levels CSV parameter and returns JSON array
func parseLevelsParameter(levelsParam string) ([]map[string]any, error) {
	levelSpecs := strings.Split(levelsParam, ",")
	var levelsJSON []map[string]any

	for _, levelSpec := range levelSpecs {
		levelSpec = strings.TrimSpace(levelSpec)
		if levelSpec == "" {
			continue
		}

		parts := strings.Split(levelSpec, ".")
		if len(parts) == 1 {
			// No version specified, use -1 to indicate latest version
			levelID := strings.TrimSpace(parts[0])
			if levelID == "" {
				return nil, fmt.Errorf("invalid level format: %s. Level ID cannot be empty", levelSpec)
			}
			levelsJSON = append(levelsJSON, map[string]any{
				"level_id":      levelID,
				"level_version": -1,
			})
		} else if len(parts) == 2 {
			// Version specified
			levelID := strings.TrimSpace(parts[0])
			levelVersionStr := strings.TrimSpace(parts[1])

			if levelID == "" || levelVersionStr == "" {
				return nil, fmt.Errorf("invalid level format: %s. Both level ID and version are required when version is specified", levelSpec)
			}

			// Parse version as integer
			levelVersion := 0
			if _, err := fmt.Sscanf(levelVersionStr, "%d", &levelVersion); err != nil {
				return nil, fmt.Errorf("invalid level version: %s. Version must be a number", levelVersionStr)
			}

			levelsJSON = append(levelsJSON, map[string]any{
				"level_id":      levelID,
				"level_version": levelVersion,
			})
		} else {
			return nil, fmt.Errorf("invalid level format: %s. Expected format: levelId or levelId.version", levelSpec)
		}
	}

	if len(levelsJSON) == 0 {
		return nil, fmt.Errorf("no valid level specifications found")
	}

	return levelsJSON, nil
}

// submitScoreHandler handles POST /score/submit requests
// Uses a stored procedure for atomic solution and score insertion
func submitScoreHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request body
		var req ScoreSubmissionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("Failed to parse score submission request: %v", err)
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Validate required fields
		if req.LevelID == "" {
			http.Error(w, "Level ID is required", http.StatusBadRequest)
			return
		}

		if req.GameVersion == "" {
			http.Error(w, "Game version is required", http.StatusBadRequest)
			return
		}

		if req.Solution == "" {
			http.Error(w, "Solution is required", http.StatusBadRequest)
			return
		}

		if req.SolutionHash == "" {
			http.Error(w, "Solution hash is required", http.StatusBadRequest)
			return
		}

		// Get authenticated user ID from context (set by authMiddleware)
		userID, okUserID := GetUserID(r)
		displayName, okName := GetDisplayName(r)
		friendCode, okCode := GetFriendCode(r)
		if !okUserID || !okName || !okCode {
			log.Printf("User ID, display name, or friend code not found in request context")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Verify solution hash
		if !verifySolutionHash(req.Solution, req.SolutionHash) {
			log.Printf("Rejected solution by %s (%s) for level %s.%d. Invalid hash.",
				displayName, friendCode, req.LevelID, req.LevelVersion)
			http.Error(w, "Solution hash verification failed", http.StatusBadRequest)
			return
		}

		// Validate base64 solution
		if _, err := base64.StdEncoding.DecodeString(req.Solution); err != nil {
			http.Error(w, "Invalid base64 solution format", http.StatusBadRequest)
			return
		}

		// Convert scores map to JSON array format expected by the stored procedure
		var scoresJSON []map[string]any
		for scoreType, scoreValue := range req.Scores {
			scoresJSON = append(scoresJSON, map[string]any{
				"type":  scoreType,
				"value": scoreValue,
			})
		}

		scoresJSONBytes, err := json.Marshal(scoresJSON)
		if err != nil {
			log.Printf("Failed to marshal scores to JSON: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Call stored procedure to submit solution with scores
		var solutionID string
		err = db.QueryRow(`
			SELECT submit_solution_with_scores($1, $2, $3, $4, $5, $6, $7)
		`, userID, req.LevelID, req.LevelVersion, req.GameVersion, req.Solution, 1, string(scoresJSONBytes)).
			Scan(&solutionID)

		if err != nil {
			log.Printf("Rejected solution by %s (%s) for level %s.%d. %v",
				displayName, friendCode, req.LevelID, req.LevelVersion, err)
			http.Error(w, "Failed to save solution", http.StatusInternalServerError)
			return
		}

		log.Printf("Accepted solution by %s (%s) for level %s.%d. Solution ID: %s.",
			displayName, friendCode, req.LevelID, req.LevelVersion, solutionID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Solution{
			ApiResponse: ApiResponse{
				Message: "Score submitted successfully",
			},
			SolutionID: solutionID,
		})
	}
}

// verifySolutionHash verifies that the provided hash matches the solution with the secret salt
func verifySolutionHash(solution, providedHash string) bool {
	salt := os.Getenv("SOLUTION_SALT")
	if salt == "" {
		log.Printf("Warning: SOLUTION_SALT not configured")
		return false
	}

	// Create the salted solution string
	saltedSolution := solution + salt

	// Calculate hash
	hash := sha256.Sum256([]byte(saltedSolution))
	expectedHash := fmt.Sprintf("%x", hash)

	// Compare hashes (case-insensitive)
	return strings.EqualFold(expectedHash, providedHash)
}

// bestScoresHandler handles GET /score/best requests
// Returns best scores for specified levels within the given scope
func bestScoresHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get authenticated user ID from context (set by authMiddleware)
		userID, ok := GetUserID(r)
		if !ok {
			log.Printf("User ID not found in request context")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Parse query parameters
		levelsParam := r.URL.Query().Get("levels")
		scope := r.URL.Query().Get("scope")

		// Default scope to global if not specified
		if scope == "" {
			scope = "global"
		}

		// Validate scope
		validScopes := map[string]bool{
			"personal": true,
			"friends":  true,
			"regional": true,
			"global":   true,
		}
		if !validScopes[scope] {
			http.Error(w, "Invalid scope. Must be: personal, friends, regional, or global", http.StatusBadRequest)
			return
		}

		// Parse levels parameter if provided, otherwise pass NULL for all levels
		var levelsArg interface{}
		if levelsParam != "" {
			levelsJSON, err := parseLevelsParameter(levelsParam)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			levelsJSONBytes, err := json.Marshal(levelsJSON)
			if err != nil {
				log.Printf("Failed to marshal levels to JSON: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			levelsArg = string(levelsJSONBytes)
		}

		// Call stored procedure to get best scores
		rows, err := db.Query(`
			SELECT * FROM get_best_scores($1, $2, $3)
		`, userID, levelsArg, scope)

		if err != nil {
			log.Printf("Failed to get best scores: %v", err)
			// Check for specific error messages from stored procedure
			if strings.Contains(err.Error(), "not yet implemented") {
				http.Error(w, err.Error(), http.StatusNotImplemented)
				return
			}
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var scores []BestScoreEntry
		for rows.Next() {
			var entry BestScoreEntry
			var privateUserID string
			err := rows.Scan(
				&entry.LevelID,
				&entry.LevelVersion,
				&entry.ScoreType,
				&entry.BestScore,
				&privateUserID,
				&entry.DisplayName,
				&entry.FriendCode,
			)
			if err != nil {
				log.Printf("Failed to scan best score row: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			scores = append(scores, entry)
		}

		if err = rows.Err(); err != nil {
			log.Printf("Error iterating over best score rows: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		response := BestScoresResponse{
			Scores: scores,
			Count:  len(scores),
			Scope:  scope,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Failed to encode best scores response: %v", err)
		}
	}
}

// leaderboardHandler handles GET /score/leaderboard requests
// Returns paginated leaderboard scores for specified levels within the given scope
func leaderboardHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get authenticated user ID from context (set by authMiddleware)
		userID, ok := GetUserID(r)
		if !ok {
			log.Printf("User ID not found in request context")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Parse query parameters
		levelsParam := r.URL.Query().Get("levels")
		scope := r.URL.Query().Get("scope")
		scoreTypeParam := r.URL.Query().Get("score_type")
		offsetParam := r.URL.Query().Get("offset")
		sizeParam := r.URL.Query().Get("size")

		// Validate levels parameter
		if levelsParam == "" {
			http.Error(w, "Levels parameter is required", http.StatusBadRequest)
			return
		}

		// Default scope to global if not specified
		if scope == "" {
			scope = "global"
		}

		// Validate scope
		validScopes := map[string]bool{
			"personal": true,
			"friends":  true,
			"regional": true,
			"global":   true,
		}
		if !validScopes[scope] {
			http.Error(w, "Invalid scope. Must be: personal, friends, regional, or global", http.StatusBadRequest)
			return
		}

		// Validate score_type parameter (optional)
		// If not provided, returns all score types
		if scoreTypeParam != "" {
			// Basic validation - check it's not empty after trimming
			scoreTypeParam = strings.TrimSpace(scoreTypeParam)
			if scoreTypeParam == "" {
				http.Error(w, "Score type cannot be empty", http.StatusBadRequest)
				return
			}
		}

		// Parse pagination parameters
		offset := 0
		if offsetParam != "" {
			if _, err := fmt.Sscanf(offsetParam, "%d", &offset); err != nil || offset < 0 {
				http.Error(w, "Invalid offset parameter. Must be a non-negative integer", http.StatusBadRequest)
				return
			}
		}

		size := 20 // Default page size
		if sizeParam != "" {
			if _, err := fmt.Sscanf(sizeParam, "%d", &size); err != nil || size <= 0 || size > 100 {
				http.Error(w, "Invalid size parameter. Must be between 1 and 100", http.StatusBadRequest)
				return
			}
		}

		// Parse levels parameter and build JSON array
		levelsJSON, err := parseLevelsParameter(levelsParam)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Convert levels to JSON
		levelsJSONBytes, err := json.Marshal(levelsJSON)
		if err != nil {
			log.Printf("Failed to marshal levels to JSON: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Call stored procedure to get leaderboard scores
		// First get total count for pagination
		var totalCount int
		err = db.QueryRow(`
			SELECT get_leaderboard_count($1, $2, $3, $4)
		`, userID, string(levelsJSONBytes), scope, scoreTypeParam).Scan(&totalCount)

		if err != nil {
			log.Printf("Failed to get leaderboard count: %v", err)
			// If stored procedure doesn't exist yet, return a helpful error
			if strings.Contains(err.Error(), "does not exist") {
				http.Error(w, "Leaderboard functionality not yet implemented in database", http.StatusNotImplemented)
				return
			}
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Get paginated results
		rows, err := db.Query(`
			SELECT * FROM get_leaderboard($1, $2, $3, $4, $5, $6)
		`, userID, string(levelsJSONBytes), scope, scoreTypeParam, offset, size)

		if err != nil {
			log.Printf("Failed to get leaderboard: %v", err)
			// Check for specific error messages from stored procedure
			if strings.Contains(err.Error(), "not yet implemented") {
				http.Error(w, err.Error(), http.StatusNotImplemented)
				return
			}
			if strings.Contains(err.Error(), "does not exist") {
				http.Error(w, "Leaderboard functionality not yet implemented in database", http.StatusNotImplemented)
				return
			}
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var scores []LeaderboardEntry
		for rows.Next() {
			var entry LeaderboardEntry
			var privateUserID string
			err := rows.Scan(
				&entry.Rank,
				&entry.LevelID,
				&entry.LevelVersion,
				&entry.ScoreType,
				&entry.BestScore,
				&privateUserID,
				&entry.DisplayName,
				&entry.FriendCode,
			)
			if err != nil {
				log.Printf("Failed to scan leaderboard row: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			scores = append(scores, entry)
		}

		if err = rows.Err(); err != nil {
			log.Printf("Error iterating over leaderboard rows: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		response := LeaderboardResponse{
			Scores: scores,
			Count:  len(scores),
			Scope:  scope,
			Pagination: PaginationInfo{
				Offset: offset,
				Size:   size,
				Total:  totalCount,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Failed to encode leaderboard response: %v", err)
		}
	}
}
