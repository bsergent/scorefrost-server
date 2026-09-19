package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestParseLevelsParameter(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedFirst map[string]any
		expectedError bool
	}{
		{
			name:          "Valid single level",
			input:         "test.1",
			expectedCount: 1,
			expectedFirst: map[string]any{"level_id": "test", "level_version": 1},
			expectedError: false,
		},
		{
			name:          "Valid multiple levels",
			input:         "test.1,level2.2",
			expectedCount: 2,
			expectedFirst: map[string]any{"level_id": "test", "level_version": 1},
			expectedError: false,
		},
		{
			name:          "Valid latest version",
			input:         "test.-1",
			expectedCount: 1,
			expectedFirst: map[string]any{"level_id": "test", "level_version": -1},
			expectedError: false,
		},
		{
			name:          "Valid level without version (defaults to latest)",
			input:         "test",
			expectedCount: 1,
			expectedFirst: map[string]any{"level_id": "test", "level_version": -1},
			expectedError: false,
		},
		{
			name:          "Invalid format - empty",
			input:         "",
			expectedError: true,
		},
		{
			name:          "Invalid format - non-numeric version",
			input:         "test.abc",
			expectedError: true,
		},
		{
			name:          "Mixed valid formats",
			input:         "test.1,level2",
			expectedCount: 2,
			expectedFirst: map[string]any{"level_id": "test", "level_version": 1},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLevelsParameter(tt.input)

			if tt.expectedError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(result) != tt.expectedCount {
				t.Errorf("Expected %d levels, got %d", tt.expectedCount, len(result))
				return
			}

			if tt.expectedCount > 0 {
				first := result[0]
				if first["level_id"] != tt.expectedFirst["level_id"] {
					t.Errorf("Expected level_id %v, got %v", tt.expectedFirst["level_id"], first["level_id"])
				}
				if first["level_version"] != tt.expectedFirst["level_version"] {
					t.Errorf("Expected level_version %v, got %v", tt.expectedFirst["level_version"], first["level_version"])
				}
			}
		})
	}
}

func TestLeaderboardResponseStructure(t *testing.T) {
	// Test the leaderboard response structure types
	entry := LeaderboardEntry{
		BestScoreEntry: BestScoreEntry{
			LevelID:      "test",
			LevelVersion: 1,
			ScoreType:    "time_ms",
			BestScore:    15000,
			DisplayName:  "Test User",
			FriendCode:   "ABC-123",
		},
		Rank: 1,
	}

	if entry.Rank != 1 {
		t.Errorf("Expected rank 1, got %d", entry.Rank)
	}
	if entry.ScoreType != "time_ms" {
		t.Errorf("Expected score type 'time_ms', got '%s'", entry.ScoreType)
	}

	// Test pagination structure
	pagination := PaginationInfo{
		Offset: 0,
		Size:   50,
		Total:  100,
	}

	if pagination.Total != 100 {
		t.Errorf("Expected total 100, got %d", pagination.Total)
	}

	// Test full response structure
	response := LeaderboardResponse{
		Scores:     []LeaderboardEntry{entry},
		Count:      1,
		Pagination: pagination,
	}

	if response.Count != 1 {
		t.Errorf("Expected count 1, got %d", response.Count)
	}
	if len(response.Scores) != 1 {
		t.Errorf("Expected 1 score, got %d", len(response.Scores))
	}
}

func TestSubmitScoreHandler_TouchesActiveTimeOnSuccessfulSubmission(t *testing.T) {
	t.Setenv("SOLUTION_SALT", "test-salt")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock database: %v", err)
	}
	defer db.Close()

	userID := uuid.New()
	solution := "dGVzdA=="
	solutionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(solution+"test-salt")))

	mock.ExpectQuery(`SELECT submit_solution_with_scores\(\$1, \$2, \$3, \$4, \$5, \$6, \$7\)`).
		WithArgs(userID, "level-1", 1, "1.0.0", solution, 1, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"submit_solution_with_scores"}).AddRow("sol-123"))

	mock.ExpectExec(`SELECT touch_user_active_time\(\$1, \$2\)`).
		WithArgs(userID, "1.0.0").
		WillReturnResult(sqlmock.NewResult(1, 1))

	handler := submitScoreHandler(db)

	body := map[string]any{
		"level_id":      "level-1",
		"level_version": 1,
		"game_version":  "1.0.0",
		"solution":      solution,
		"solution_hash": solutionHash,
		"scores": map[string]int{
			"time_ms": 123,
		},
	}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, APIBasePath+"/score", bytes.NewBuffer(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), contextKeyUserID, UserID(userID)))
	req = req.WithContext(context.WithValue(req.Context(), contextKeyDisplayName, DisplayName("Tester")))
	req = req.WithContext(context.WithValue(req.Context(), contextKeyFriendCode, FriendCode("ABCD-EFGH")))

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Expected status %d, got %d; body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("Unmet SQL expectations: %v", err)
	}
}
