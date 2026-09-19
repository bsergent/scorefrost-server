//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Integration tests for ScoreFrost API

func TestIntegrationUserCreation(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Test creating a new user (no input required)
	user, err := createIntegrationTestUser(server.URL)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Verify user has all required fields
	if user.ID == "" {
		t.Error("User ID is empty")
	}
	if user.FriendCode == "" {
		t.Error("Friend code is empty")
	}
	if user.DisplayName == "" {
		t.Error("Display name is empty")
	}
	if user.APIKey == "" {
		t.Error("API key is empty")
	}

	// Verify friend code format (XXXX-XXXX)
	if len(user.FriendCode) != 9 || user.FriendCode[4] != '-' {
		t.Errorf("Invalid friend code format: %s", user.FriendCode)
	}
}

func TestIntegrationUserAuthentication(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// First create a new user
	newUser, err := createIntegrationTestUser(server.URL)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Now authenticate with the API key from the new user
	authenticatedUser, err := authenticateIntegrationTestUser(server.URL, newUser.ID, newUser.APIKey)
	if err != nil {
		t.Fatalf("Failed to authenticate user: %v", err)
	}

	// Verify the authenticated user matches the original user
	if authenticatedUser.ID != newUser.ID {
		t.Errorf("Authenticated user ID mismatch: got %s, want %s", authenticatedUser.ID, newUser.ID)
	}
	if authenticatedUser.FriendCode != newUser.FriendCode {
		t.Errorf("Authenticated user friend code mismatch: got %s, want %s", authenticatedUser.FriendCode, newUser.FriendCode)
	}
	if authenticatedUser.DisplayName != newUser.DisplayName {
		t.Errorf("Authenticated user display name mismatch: got %s, want %s", authenticatedUser.DisplayName, newUser.DisplayName)
	}

	// API key should not be included in authentication response
	if authenticatedUser.APIKey != "" {
		t.Error("API key should not be included in authentication response")
	}

	// Test authentication with invalid API key
	_, err = authenticateIntegrationTestUser(server.URL, newUser.ID, "invalid-api-key")
	if err == nil {
		t.Error("Authentication with invalid API key should fail")
	}
}

func TestIntegrationScoreSubmission(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Prepare test data
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_001",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms":  15000,
			"striping": 8,
			"fuel_rem": 45,
		},
	}

	// Submit score for first user
	response, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request)
	if err != nil {
		t.Fatalf("Failed to submit score: %v", err)
	}

	// Verify response
	if response.SolutionID == "" {
		t.Error("Empty solution ID returned")
	} else if _, err := uuid.Parse(response.SolutionID); err != nil {
		t.Errorf("Invalid UUID solution ID returned: %s", response.SolutionID)
	}

	// Submit a better score for second user
	betterRequest := request
	betterRequest.Scores = map[string]int{
		"time_ms":  12000,
		"striping": 10,
		"fuel_rem": 55,
	}

	betterResponse, err := submitIntegrationScore(server, integrationConfig.TestAPIKey2, betterRequest)
	if err != nil {
		t.Fatalf("Failed to submit better score: %v", err)
	}

	if betterResponse.SolutionID == "" {
		t.Error("Empty solution ID returned for better score")
	} else if _, err := uuid.Parse(betterResponse.SolutionID); err != nil {
		t.Errorf("Invalid UUID solution ID returned for better score: %s", betterResponse.SolutionID)
	}
}

func TestIntegrationScoreSubmissionIgnoresUnknownScoreTypes(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "VGVzdCBVbmtub3duIFNjb3JlIFR5cGU=" // "Test Unknown Score Type" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "unk_type_001",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 11111,
			"unk_met": 42,
		},
	}

	response, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request)
	if err != nil {
		t.Fatalf("Expected submission to succeed while ignoring unknown score types, got error: %v", err)
	}

	if response.SolutionID == "" {
		t.Error("Empty solution ID returned")
	}

	bestScores, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"unk_type_001.1"}, "personal")
	if err != nil {
		t.Fatalf("Failed to get best scores: %v", err)
	}

	assertIntegrationScoreExists(t, bestScores.Scores, "unk_type_001", "time_ms", 11111)

	for _, score := range bestScores.Scores {
		if score.LevelID == "unk_type_001" && score.ScoreType == "unk_met" {
			t.Fatalf("Unknown score type should have been ignored, but found persisted score: %+v", score)
		}
	}
}

func TestIntegrationBestScoresGlobal(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// First submit some scores to ensure we have data
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request1 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_002",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 20000,
			"stars":   2,
		},
	}

	request2 := request1
	request2.Scores = map[string]int{
		"time_ms": 18000, // Better time
		"stars":   3,     // Better stars
	}

	// Submit scores for both users
	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request1)
	if err != nil {
		t.Fatalf("Failed to submit first score: %v", err)
	}

	_, err = submitIntegrationScore(server, integrationConfig.TestAPIKey2, request2)
	if err != nil {
		t.Fatalf("Failed to submit second score: %v", err)
	}

	// Get best scores
	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"test_level_002.1"}, "global")
	if err != nil {
		t.Fatalf("Failed to get best scores: %v", err)
	}

	// Verify response structure
	if response.Scope != "global" {
		t.Errorf("Expected scope 'global', got '%s'", response.Scope)
	}

	// Verify count field matches array length
	assertIntegrationScoreCount(t, response, len(response.Scores))

	// Verify we have the better scores
	assertIntegrationScoreExists(t, response.Scores, "test_level_002", "time_ms", 18000)
	assertIntegrationScoreExists(t, response.Scores, "test_level_002", "stars", 3)

	// Verify the better scores belong to user 2
	assertIntegrationScoreFriendCode(t, response.Scores, "test_level_002", "time_ms", integrationConfig.TestFriendCode2)
	assertIntegrationScoreFriendCode(t, response.Scores, "test_level_002", "stars", integrationConfig.TestFriendCode2)
}

func TestIntegrationBestScoresGlobalTieBreakByEarliestSubmission(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "VGlueSBUaWUgQnJlYWsgVGVzdA==" // "Tiny Tie Break Test" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "tie_break_glob1",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"fuel_rem": 12,
		},
	}

	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request); err != nil {
		t.Fatalf("Failed to submit first tied score: %v", err)
	}

	// Ensure deterministic ordering for earliest-submission tie-break.
	time.Sleep(10 * time.Millisecond)

	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey2, request); err != nil {
		t.Fatalf("Failed to submit second tied score: %v", err)
	}

	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"tie_break_glob1.1"}, "global")
	if err != nil {
		t.Fatalf("Failed to get global best scores for tie-break test: %v", err)
	}

	if response.Scope != "global" {
		t.Errorf("Expected scope 'global', got '%s'", response.Scope)
	}

	assertIntegrationScoreCount(t, response, 1)
	assertIntegrationScoreExists(t, response.Scores, "tie_break_glob1", "fuel_rem", 12)
	assertIntegrationScoreFriendCode(t, response.Scores, "tie_break_glob1", "fuel_rem", integrationConfig.TestFriendCode)
}

func TestIntegrationBestScoresPersonal(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Submit scores for user 1
	solution := "VGVzdCBTb2x1dGlvbg==" // "Test Solution" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_003",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms":  25000,
			"fuel_rem": 60,
		},
	}

	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request)
	if err != nil {
		t.Fatalf("Failed to submit score: %v", err)
	}

	// Get personal best scores for user 1
	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"test_level_003.1"}, "personal")
	if err != nil {
		t.Fatalf("Failed to get personal best scores: %v", err)
	}

	// Verify response
	if response.Scope != "personal" {
		t.Errorf("Expected scope 'personal', got '%s'", response.Scope)
	}

	assertIntegrationScoreCount(t, response, len(response.Scores))

	// All scores should belong to user 1
	for _, score := range response.Scores {
		if score.FriendCode != integrationConfig.TestFriendCode {
			t.Errorf("Expected all scores to belong to friend code %s, found score for friend code %s", integrationConfig.TestFriendCode, score.FriendCode)
		}
	}

	assertIntegrationScoreExists(t, response.Scores, "test_level_003", "time_ms", 25000)
	assertIntegrationScoreExists(t, response.Scores, "test_level_003", "fuel_rem", 60)
}

func TestIntegrationBestScoresPersonalWithoutLevelsExcludesOtherUsers(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "U29sdXRpb24gZm9yIHBlcnNvbmFsIHNjb3BlIHRlc3Q=" // "Solution for personal scope test" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	userOneLevelA := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "pers_best_a",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 24000,
			"stars":   2,
		},
	}

	userOneLevelB := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "pers_best_b",
		LevelVersion: 3,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"fuel_rem": 55,
			"striping": 85,
		},
	}

	userTwoBetterLevelA := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "pers_best_a",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 18000,
			"stars":   4,
		},
	}

	userTwoBetterLevelB := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "pers_best_b",
		LevelVersion: 3,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"fuel_rem": 40,
			"striping": 97,
		},
	}

	for _, request := range []IntegrationScoreSubmissionRequest{userOneLevelA, userOneLevelB} {
		if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request); err != nil {
			t.Fatalf("Failed to submit user 1 score: %v", err)
		}
	}

	for _, request := range []IntegrationScoreSubmissionRequest{userTwoBetterLevelA, userTwoBetterLevelB} {
		if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey2, request); err != nil {
			t.Fatalf("Failed to submit user 2 score: %v", err)
		}
	}

	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, nil, "personal")
	if err != nil {
		t.Fatalf("Failed to get personal best scores without levels: %v", err)
	}

	if response.Scope != "personal" {
		t.Errorf("Expected scope 'personal', got '%s'", response.Scope)
	}

	assertIntegrationScoreCount(t, response, 4)
	assertIntegrationScoreExists(t, response.Scores, "pers_best_a", "time_ms", 24000)
	assertIntegrationScoreExists(t, response.Scores, "pers_best_a", "stars", 2)
	assertIntegrationScoreExists(t, response.Scores, "pers_best_b", "fuel_rem", 55)
	assertIntegrationScoreExists(t, response.Scores, "pers_best_b", "striping", 85)

	for _, score := range response.Scores {
		if score.FriendCode != integrationConfig.TestFriendCode {
			t.Errorf("Expected personal scores only for friend code %s, got friend code %s", integrationConfig.TestFriendCode, score.FriendCode)
		}
		if score.BestScore == 18000 || score.BestScore == 4 || score.BestScore == 40 || score.BestScore == 97 {
			t.Errorf("Found competing user better score in personal response: %+v", score)
		}
	}
}

func TestIntegrationBestScoresGlobalWithoutLevelsReturnsBestAcrossUsers(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "R2xvYmFsIEJlc3QgVGVzdA==" // "Global Best Test" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	userOneWorseLevelA := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "glob_best_a",
		LevelVersion: 2,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 30000,
			"stars":   1,
		},
	}

	userOneWorseLevelB := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "glob_best_b",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"fuel_rem": 50,
			"striping": 60,
		},
	}

	userTwoBetterLevelA := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "glob_best_a",
		LevelVersion: 2,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 22000,
			"stars":   3,
		},
	}

	userTwoBetterLevelB := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "glob_best_b",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"fuel_rem": 70,
			"striping": 80,
		},
	}

	for _, request := range []IntegrationScoreSubmissionRequest{userOneWorseLevelA, userOneWorseLevelB} {
		if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request); err != nil {
			t.Fatalf("Failed to submit user 1 score: %v", err)
		}
	}

	for _, request := range []IntegrationScoreSubmissionRequest{userTwoBetterLevelA, userTwoBetterLevelB} {
		if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey2, request); err != nil {
			t.Fatalf("Failed to submit user 2 score: %v", err)
		}
	}

	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, nil, "global")
	if err != nil {
		t.Fatalf("Failed to get global best scores without levels: %v", err)
	}

	if response.Scope != "global" {
		t.Errorf("Expected scope 'global', got '%s'", response.Scope)
	}

	// Verify structural integrity: count field matches array length
	assertIntegrationScoreCount(t, response, len(response.Scores))

	// User 2's better scores should appear and be attributed to them
	assertIntegrationScoreExists(t, response.Scores, "glob_best_a", "time_ms", 22000)
	assertIntegrationScoreExists(t, response.Scores, "glob_best_a", "stars", 3)
	assertIntegrationScoreExists(t, response.Scores, "glob_best_b", "fuel_rem", 70)
	assertIntegrationScoreExists(t, response.Scores, "glob_best_b", "striping", 80)

	assertIntegrationScoreFriendCode(t, response.Scores, "glob_best_a", "time_ms", integrationConfig.TestFriendCode2)
	assertIntegrationScoreFriendCode(t, response.Scores, "glob_best_a", "stars", integrationConfig.TestFriendCode2)
	assertIntegrationScoreFriendCode(t, response.Scores, "glob_best_b", "fuel_rem", integrationConfig.TestFriendCode2)
	assertIntegrationScoreFriendCode(t, response.Scores, "glob_best_b", "striping", integrationConfig.TestFriendCode2)

	// User 1's worse scores should not appear for those levels
	for _, score := range response.Scores {
		if score.LevelID == "glob_best_a" || score.LevelID == "glob_best_b" {
			if score.FriendCode == integrationConfig.TestFriendCode {
				t.Errorf("Expected user 1's scores to be beaten by user 2, but found user 1's entry in global response: %+v", score)
			}
		}
	}
}

func TestIntegrationBestScoresMultipleLevels(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Submit scores for multiple levels
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	// Level A
	requestA := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_A",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 10000,
		},
	}

	// Level B
	requestB := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_B",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 15000,
		},
	}

	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, requestA)
	if err != nil {
		t.Fatalf("Failed to submit score A: %v", err)
	}

	_, err = submitIntegrationScore(server, integrationConfig.TestAPIKey, requestB)
	if err != nil {
		t.Fatalf("Failed to submit score B: %v", err)
	}

	// Get best scores for both levels
	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"test_level_A.1", "test_level_B.1"}, "global")
	if err != nil {
		t.Fatalf("Failed to get best scores for multiple levels: %v", err)
	}

	// Should have scores for both levels
	foundA := false
	foundB := false

	for _, score := range response.Scores {
		if score.LevelID == "test_level_A" {
			foundA = true
		}
		if score.LevelID == "test_level_B" {
			foundB = true
		}
	}

	if !foundA {
		t.Error("Missing scores for test_level_A")
	}
	if !foundB {
		t.Error("Missing scores for test_level_B")
	}

	assertIntegrationScoreCount(t, response, len(response.Scores))
}

func TestIntegrationLatestVersionDetection(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Submit score for version 1
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	requestV1 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_v",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 30000,
		},
	}

	// Submit score for version 2 (higher version)
	requestV2 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "test_level_v",
		LevelVersion: 2,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 25000,
		},
	}

	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, requestV1)
	if err != nil {
		t.Fatalf("Failed to submit score v1: %v", err)
	}

	_, err = submitIntegrationScore(server, integrationConfig.TestAPIKey, requestV2)
	if err != nil {
		t.Fatalf("Failed to submit score v2: %v", err)
	}

	// Query without specifying version (should get latest = version 2)
	response, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"test_level_v"}, "global")
	if err != nil {
		t.Fatalf("Failed to get best scores with latest version: %v", err)
	}

	// Should return version 2 scores only
	for _, score := range response.Scores {
		if score.LevelVersion != 2 {
			t.Errorf("Expected version 2, got version %d", score.LevelVersion)
		}
	}

	assertIntegrationScoreExists(t, response.Scores, "test_level_v", "time_ms", 25000)
}

func TestIntegrationLeaderboard(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Submit multiple scores to create a leaderboard
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	// User 1: Good score
	request1 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "leaderboard_test",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 20000,
		},
	}

	// User 2: Better score
	request2 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "leaderboard_test",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 15000,
		},
	}

	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request1)
	if err != nil {
		t.Fatalf("Failed to submit score for user 1: %v", err)
	}

	_, err = submitIntegrationScore(server, integrationConfig.TestAPIKey2, request2)
	if err != nil {
		t.Fatalf("Failed to submit score for user 2: %v", err)
	}

	// Test global leaderboard with pagination
	response, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"leaderboard_test.1"}, "global", "", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get leaderboard: %v", err)
	}

	// Verify response structure
	if response.Scope != "global" {
		t.Errorf("Expected scope 'global', got '%s'", response.Scope)
	}

	if response.Pagination.Offset != 0 {
		t.Errorf("Expected offset 0, got %d", response.Pagination.Offset)
	}

	if response.Pagination.Size != 10 {
		t.Errorf("Expected size 10, got %d", response.Pagination.Size)
	}

	if response.Count != len(response.Scores) {
		t.Errorf("Count mismatch: expected %d, got %d", len(response.Scores), response.Count)
	}

	// Verify ranking: User 2 should be rank 1 (better time), User 1 should be rank 2
	for _, score := range response.Scores {
		if score.FriendCode == integrationConfig.TestFriendCode2 && score.Rank != 1 {
			t.Errorf("User 2 should be rank 1 (best score), got rank %d", score.Rank)
		}
		if score.FriendCode == integrationConfig.TestFriendCode && score.Rank != 2 {
			t.Errorf("User 1 should be rank 2, got rank %d", score.Rank)
		}
	}

	// Test personal leaderboard
	personalResponse, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"leaderboard_test.1"}, "personal", "", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get personal leaderboard: %v", err)
	}

	if personalResponse.Scope != "personal" {
		t.Errorf("Expected scope 'personal', got '%s'", personalResponse.Scope)
	}

	// Personal leaderboard should only contain scores for the authenticated user
	for _, score := range personalResponse.Scores {
		if score.FriendCode != integrationConfig.TestFriendCode {
			t.Errorf("Personal leaderboard should only contain scores for friend code %s, found %s", integrationConfig.TestFriendCode, score.FriendCode)
		}
	}

	// Test pagination
	paginatedResponse, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"leaderboard_test.1"}, "global", "", 1, 1)
	if err != nil {
		t.Fatalf("Failed to get paginated leaderboard: %v", err)
	}

	if paginatedResponse.Pagination.Offset != 1 {
		t.Errorf("Expected offset 1, got %d", paginatedResponse.Pagination.Offset)
	}

	if paginatedResponse.Pagination.Size != 1 {
		t.Errorf("Expected size 1, got %d", paginatedResponse.Pagination.Size)
	}

	if len(paginatedResponse.Scores) > 1 {
		t.Errorf("Expected at most 1 score with size=1, got %d", len(paginatedResponse.Scores))
	}
}

func TestIntegrationLeaderboardScoreTypeFiltering(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "dGVzdCBzb2x1dGlvbiBmb3Igc2NvcmUgdHlwZSBmaWx0ZXJpbmc=" // "test solution for score type filtering" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	// Submit scores with multiple score types for user 1
	request1 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "score_type_test",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms":  20000, // Better time than user 2 (lower is better)
			"striping": 5,     // Worse striping than user 2 (higher is better)
		},
	}

	// Submit scores with multiple score types for user 2
	request2 := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "score_type_test",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms":  25000, // Worse time than user 1 (lower is better)
			"striping": 8,     // Better striping than user 1 (higher is better)
		},
	}

	// Submit scores for both users
	_, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request1)
	if err != nil {
		t.Fatalf("Failed to submit score for user 1: %v", err)
	}

	_, err = submitIntegrationScore(server, integrationConfig.TestAPIKey2, request2)
	if err != nil {
		t.Fatalf("Failed to submit score for user 2: %v", err)
	}

	// Test filtering by time_ms - should return only time scores
	timeResponse, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"score_type_test.1"}, "global", "time_ms", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get time_ms leaderboard: %v", err)
	}

	// Verify all returned scores are time_ms type
	for _, score := range timeResponse.Scores {
		if score.ScoreType != "time_ms" {
			t.Errorf("Expected score_type 'time_ms', got '%s'", score.ScoreType)
		}
	}

	// Verify ranking: User 1 should be rank 1 (better/lower time)
	if len(timeResponse.Scores) >= 2 {
		user1Found := false
		user2Found := false
		for _, score := range timeResponse.Scores {
			if score.FriendCode == integrationConfig.TestFriendCode {
				user1Found = true
				if score.Rank != 1 {
					t.Errorf("User 1 should be rank 1 for time_ms, got rank %d", score.Rank)
				}
			}
			if score.FriendCode == integrationConfig.TestFriendCode2 {
				user2Found = true
				if score.Rank != 2 {
					t.Errorf("User 2 should be rank 2 for time_ms, got rank %d", score.Rank)
				}
			}
		}
		if !user1Found || !user2Found {
			t.Error("Both users should appear in time_ms leaderboard")
		}
	}

	// Test filtering by striping - should return only striping scores
	stripingResponse, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"score_type_test.1"}, "global", "striping", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get striping leaderboard: %v", err)
	}

	// Verify all returned scores are striping type
	for _, score := range stripingResponse.Scores {
		if score.ScoreType != "striping" {
			t.Errorf("Expected score_type 'striping', got '%s'", score.ScoreType)
		}
	}

	// Verify ranking: User 2 should be rank 1 (higher striping is better)
	if len(stripingResponse.Scores) >= 2 {
		user1Found := false
		user2Found := false
		for _, score := range stripingResponse.Scores {
			if score.FriendCode == integrationConfig.TestFriendCode2 {
				user2Found = true
				if score.Rank != 1 {
					t.Errorf("User 2 should be rank 1 for striping, got rank %d", score.Rank)
				}
			}
			if score.FriendCode == integrationConfig.TestFriendCode {
				user1Found = true
				if score.Rank != 2 {
					t.Errorf("User 1 should be rank 2 for striping, got rank %d", score.Rank)
				}
			}
		}
		if !user1Found || !user2Found {
			t.Error("Both users should appear in striping leaderboard")
		}
	}

	// Test without score_type filter - should return all scores
	allResponse, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"score_type_test.1"}, "global", "", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get all scores leaderboard: %v", err)
	}

	// Should contain both time_ms and striping scores - one from each user showing their best
	timeScoresFound := 0
	stripingScoresFound := 0
	for _, score := range allResponse.Scores {
		if score.ScoreType == "time_ms" {
			timeScoresFound++
		} else if score.ScoreType == "striping" {
			stripingScoresFound++
		}
	}

	if timeScoresFound != 2 {
		t.Errorf("Expected 2 time_ms scores (one per user), got %d", timeScoresFound)
	}
	if stripingScoresFound != 2 {
		t.Errorf("Expected 2 striping scores (one per user), got %d", stripingScoresFound)
	}
}

// Integration tests for enhanced login with user_id and api_key recovery

func TestIntegrationLoginWithValidUserIDAndAPIKey(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// First create a user to get their internal ID and API key
	originalUser, err := createIntegrationTestUser(server.URL)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Now authenticate with user_id and api_key
	user, statusCode, err := loginWithUserIDAndAPIKey(server.URL, originalUser.ID, originalUser.APIKey)
	if err != nil {
		t.Fatalf("Failed to login with internal user ID and API key: %v", err)
	}

	// Should return 200 OK
	if statusCode != 200 {
		t.Errorf("Expected status code 200, got %d", statusCode)
	}

	// Verify the user matches the original
	if user.ID != originalUser.ID {
		t.Errorf("User ID mismatch: got %s, want %s", user.ID, originalUser.ID)
	}

	// API key should NOT be included in response for existing user
	if user.APIKey != "" {
		t.Error("API key should not be included for existing user")
	}
}

func TestIntegrationLoginWithUserIDNoAPIKey(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	requestedUserID := uuid.New().String()

	// No api_key means create user flow; provided user_id is ignored
	user, statusCode, err := loginWithUserID(server.URL, requestedUserID)
	if err != nil {
		t.Fatalf("Failed to create user without API key: %v", err)
	}

	// Should return 201 Created
	if statusCode != 201 {
		t.Errorf("Expected status code 201, got %d", statusCode)
	}

	if user.APIKey == "" {
		t.Error("API key should be included for newly created user")
	}

	if user.ID == requestedUserID {
		t.Error("Expected server to ignore provided user_id when API key is missing")
	}

	if _, parseErr := uuid.Parse(user.ID); parseErr != nil {
		t.Errorf("Expected created user ID to be a valid UUID, got %q", user.ID)
	}
}

func TestIntegrationLoginWithUserIDInvalidAPIKey(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Create a user first
	originalUser, err := createIntegrationTestUser(server.URL)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Try to login with user_id but wrong api_key
	_, statusCode, err := loginWithUserIDAndAPIKey(server.URL, originalUser.ID, "wrong-api-key")

	// Should return 401 Unauthorized
	if statusCode != 401 {
		t.Errorf("Expected status code 401, got %d", statusCode)
	}
}

func TestIntegrationLoginWithInvalidUUIDFormat(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Try to login with invalid UUID format
	_, statusCode, _ := loginWithUserIDAndAPIKey(server.URL, "not-a-valid-uuid", "some-api-key")

	// Should return 400 Bad Request
	if statusCode != 400 {
		t.Errorf("Expected status code 400, got %d", statusCode)
	}
}

func TestIntegrationLoginReclaimUserWithNonexistentID(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Generate a fresh UUID for reclamation testing - ensures it doesn't exist
	reclaimUUID := uuid.New().String()

	// Try to login with user_id (nonexistent) - should create/reclaim with new API key
	user, statusCode, err := loginWithUserIDAndAPIKey(server.URL, reclaimUUID, "some-api-key-ignored")
	if err != nil {
		t.Fatalf("Failed to reclaim user: %v", err)
	}

	// Should return 201 Created
	if statusCode != 201 {
		t.Errorf("Expected status code 201, got %d", statusCode)
	}

	// Verify user was created with the requested internal ID
	if user.ID != reclaimUUID {
		t.Errorf("User ID mismatch: got %s, want %s", user.ID, reclaimUUID)
	}

	// API key should be included for new/reclaimed user
	if user.APIKey == "" {
		t.Error("API key should be included for reclaimed user")
	}

	// API key should be different from the one passed in (we generate a new one)
	if user.APIKey == "some-api-key-ignored" {
		t.Error("Generated API key should be different from the ignored input")
	}
}

func TestIntegrationLoginReclaimUserWithRequestedFriendCode(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	reclaimUUID := uuid.New().String()
	requestedFriendCode := "ABCD-EF01"

	user, statusCode, err := loginWithUserIDAndAPIKeyAndFriendCode(server.URL, reclaimUUID, "some-api-key-ignored", requestedFriendCode)
	if err != nil {
		t.Fatalf("Failed to reclaim user with requested friend code: %v", err)
	}

	if statusCode != 201 {
		t.Errorf("Expected status code 201, got %d", statusCode)
	}

	if user.ID != reclaimUUID {
		t.Errorf("User ID mismatch: got %s, want %s", user.ID, reclaimUUID)
	}

	if user.FriendCode != requestedFriendCode {
		t.Errorf("Friend code mismatch: got %s, want %s", user.FriendCode, requestedFriendCode)
	}
}

func TestIntegrationLoginReclaimUserWithInvalidFriendCodeFallsBackToGeneratedCode(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	reclaimUUID := uuid.New().String()
	invalidFriendCode := "not-a-code"

	user, statusCode, err := loginWithUserIDAndAPIKeyAndFriendCode(server.URL, reclaimUUID, "some-api-key-ignored", invalidFriendCode)
	if err != nil {
		t.Fatalf("Failed to reclaim user with invalid friend code: %v", err)
	}

	if statusCode != 201 {
		t.Errorf("Expected status code 201, got %d", statusCode)
	}

	if user.FriendCode == invalidFriendCode {
		t.Fatalf("Expected invalid friend code %q to be replaced", invalidFriendCode)
	}

	if !friendCodeRegex.MatchString(user.FriendCode) {
		t.Fatalf("Expected fallback friend code to match required format, got %q", user.FriendCode)
	}
}

func TestIntegrationLoginReclaimUserWithDisplayNameCreatesPendingEntry(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	reclaimUUID := uuid.New()
	requestedDisplayName := DisplayName("  Reclaimed_Name_999  ")

	requestBody := map[string]interface{}{
		"game_id":      "com.company.testgame",
		"game_version": "1.0.0",
		"user_id":      reclaimUUID,
		"display_name": requestedDisplayName,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+APIBasePath+"/user", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer some-api-key-ignored")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("Failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status code %d, got %d", http.StatusCreated, resp.StatusCode)
	}

	var reclaimedUser IntegrationTestUser
	if err := json.NewDecoder(resp.Body).Decode(&reclaimedUser); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if reclaimedUser.ID != reclaimUUID.String() {
		t.Fatalf("User ID mismatch: got %s, want %s", reclaimedUser.ID, reclaimUUID.String())
	}

	var currentDisplayName string
	var pendingDisplayName string
	var displayNameStatus int
	err = db.QueryRow(`
		SELECT
			COALESCE(display_name, ''),
			COALESCE(display_name_pending, ''),
			display_name_status
		FROM "user"
		WHERE id = $1
	`, reclaimUUID).Scan(&currentDisplayName, &pendingDisplayName, &displayNameStatus)
	if err != nil {
		t.Fatalf("Failed to query reclaimed user row: %v", err)
	}

	expectedPendingDisplayName, err := sanitizeDisplayName(requestedDisplayName)
	if err != nil {
		t.Fatalf("Test setup produced invalid display name: %v", err)
	}

	if currentDisplayName == "" {
		t.Fatal("Expected reclaimed user to have a generated current display_name")
	}

	if pendingDisplayName != string(expectedPendingDisplayName) {
		t.Fatalf("Pending display_name mismatch: got %q, want %q", pendingDisplayName, expectedPendingDisplayName)
	}

	if currentDisplayName == pendingDisplayName {
		t.Fatalf("Expected current display_name (%q) to differ from pending display_name (%q)", currentDisplayName, pendingDisplayName)
	}

	if displayNameStatus != int(DisplayNameStatusPending) {
		t.Fatalf("Expected display_name_status %d (pending), got %d", DisplayNameStatusPending, displayNameStatus)
	}
}

func TestIntegrationLoginReclaimDeletedUser(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Create a user and submit a score before deleting the account
	originalUser, err := createIntegrationTestUser(server.URL)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Submit a score
	solution := "SGVsbG8gV29ybGQ=" // "Hello World" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "recovery_level",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores: map[string]int{
			"time_ms": 10000,
		},
	}

	_, err = submitIntegrationScore(server, originalUser.APIKey, request)
	if err != nil {
		t.Fatalf("Failed to submit score: %v", err)
	}

	// Delete the user row so the login flow must reclaim the account
	// Note that this cascades and deletes the score as well
	result, err := db.Exec(`
		DELETE FROM "user"
		WHERE id = $1
	`, originalUser.ID)
	if err != nil {
		t.Fatalf("Failed to delete user before reclaim test: %v", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("Failed to check deleted rows before reclaim test: %v", err)
	}
	if rowsAffected != 1 {
		t.Fatalf("Expected to delete 1 user before reclaim test, deleted %d", rowsAffected)
	}

	// Test that the deleted user can be reclaimed successfully
	// Reclaim the account using the original user ID and API key
	authenticatedUser, statusCode, err := loginWithUserIDAndAPIKey(server.URL, originalUser.ID, originalUser.APIKey)
	if err != nil {
		t.Fatalf("Failed to reclaim user: %v", err)
	}

	// Should return 201 Created for a reclaimed user
	if statusCode != 201 {
		t.Errorf("Expected status code 201, got %d", statusCode)
	}

	// Verify reclaimed user is not nil and has the same user ID
	if authenticatedUser == nil {
		t.Fatalf("authenticatedUser is nil after reclaim")
	}
	if authenticatedUser.ID != originalUser.ID {
		t.Errorf("Reclaimed user ID mismatch: got %s, want %s", authenticatedUser.ID, originalUser.ID)
	}
	if authenticatedUser.APIKey == "" {
		t.Error("API key should be returned for reclaimed user")
	}
	if authenticatedUser.APIKey == originalUser.APIKey {
		t.Error("Reclaimed user should receive a new API key")
	}
}

// TestIntegrationResultOnlyPassCountsInBestScores submits pass, fail, and
// abandon attempts for the same level, with the fail and abandon scores being
// numerically better (lower time_ms). Verifies that only the pass score appears
// in both the personal and global best-scores responses.
func TestIntegrationResultOnlyPassCountsInBestScores(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "UGFzc0ZhaWxBYmFuZG9uQmVzdA==" // "PassFailAbandonBest" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	base := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "result_best_001",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
	}

	// fail: best time (lowest) — should NOT count
	failReq := base
	failReq.Result = "fail"
	failReq.Scores = map[string]int{"time_ms": 1000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, failReq); err != nil {
		t.Fatalf("Failed to submit fail score: %v", err)
	}

	// abandon: second-best time — should NOT count
	abandonReq := base
	abandonReq.Result = "abandon"
	abandonReq.Scores = map[string]int{"time_ms": 2000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, abandonReq); err != nil {
		t.Fatalf("Failed to submit abandon score: %v", err)
	}

	// pass: worst time — the ONLY one that should appear
	passReq := base
	passReq.Result = "pass"
	passReq.Scores = map[string]int{"time_ms": 5000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, passReq); err != nil {
		t.Fatalf("Failed to submit pass score: %v", err)
	}

	// Personal best scores: expect exactly the pass score (5000)
	personal, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"result_best_001.1"}, "personal")
	if err != nil {
		t.Fatalf("Failed to get personal best scores: %v", err)
	}
	assertIntegrationScoreExists(t, personal.Scores, "result_best_001", "time_ms", 5000)

	// Global best scores: expect exactly the pass score (5000)
	global, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"result_best_001.1"}, "global")
	if err != nil {
		t.Fatalf("Failed to get global best scores: %v", err)
	}
	assertIntegrationScoreExists(t, global.Scores, "result_best_001", "time_ms", 5000)
}

// TestIntegrationResultOnlyPassCountsInLeaderboard submits pass, fail, and
// abandon attempts for the same level, with the fail and abandon scores being
// numerically better (lower time_ms). Verifies that only the pass score appears
// in both the personal and global leaderboard responses.
func TestIntegrationResultOnlyPassCountsInLeaderboard(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "UGFzc0ZhaWxBYmFuZG9uTGVhZA==" // "PassFailAbandonLead" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	base := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "result_lb_001",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
	}

	// fail: best time — should NOT count
	failReq := base
	failReq.Result = "fail"
	failReq.Scores = map[string]int{"time_ms": 1000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, failReq); err != nil {
		t.Fatalf("Failed to submit fail score: %v", err)
	}

	// abandon: second-best time — should NOT count
	abandonReq := base
	abandonReq.Result = "abandon"
	abandonReq.Scores = map[string]int{"time_ms": 2000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, abandonReq); err != nil {
		t.Fatalf("Failed to submit abandon score: %v", err)
	}

	// pass: worst time — the ONLY one that should appear
	passReq := base
	passReq.Result = "pass"
	passReq.Scores = map[string]int{"time_ms": 5000}
	if _, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, passReq); err != nil {
		t.Fatalf("Failed to submit pass score: %v", err)
	}

	// Personal leaderboard: expect exactly one entry with the pass score (5000)
	personal, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"result_lb_001.1"}, "personal", "time_ms", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get personal leaderboard: %v", err)
	}
	if len(personal.Scores) != 1 {
		t.Fatalf("Personal leaderboard: expected 1 entry, got %d", len(personal.Scores))
	}
	if personal.Scores[0].BestScore != 5000 {
		t.Errorf("Personal leaderboard: expected best_score=5000 (pass), got %d", personal.Scores[0].BestScore)
	}

	// Global leaderboard: expect exactly one entry with the pass score (5000)
	global, err := getIntegrationLeaderboard(server, integrationConfig.TestAPIKey, []string{"result_lb_001.1"}, "global", "time_ms", 0, 10)
	if err != nil {
		t.Fatalf("Failed to get global leaderboard: %v", err)
	}
	if len(global.Scores) != 1 {
		t.Fatalf("Global leaderboard: expected 1 entry, got %d", len(global.Scores))
	}
	if global.Scores[0].BestScore != 5000 {
		t.Errorf("Global leaderboard: expected best_score=5000 (pass), got %d", global.Scores[0].BestScore)
	}
}

// TestIntegrationOmittedResultDefaultsToPass verifies that when the result
// field is absent from the request body, the submission is treated as a pass
// and the score appears in the personal best-scores response.
func TestIntegrationOmittedResultDefaultsToPass(t *testing.T) {
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	solution := "T21pdHRlZFJlc3VsdA==" // "OmittedResult" in base64
	solutionHash := calculateIntegrationSolutionHash(solution)

	// No Result field set — omitted from JSON
	request := IntegrationScoreSubmissionRequest{
		Solution:     solution,
		SolutionHash: solutionHash,
		LevelID:      "result_omit_001",
		LevelVersion: 1,
		GameVersion:  "1.0.0",
		Scores:       map[string]int{"time_ms": 9999},
	}

	resp, err := submitIntegrationScore(server, integrationConfig.TestAPIKey, request)
	if err != nil {
		t.Fatalf("Expected submission to succeed, got: %v", err)
	}
	if resp.SolutionID == "" {
		t.Fatal("Empty solution ID returned")
	}

	bestScores, err := getIntegrationBestScores(server, integrationConfig.TestAPIKey, []string{"result_omit_001.1"}, "personal")
	if err != nil {
		t.Fatalf("Failed to get personal best scores: %v", err)
	}
	assertIntegrationScoreExists(t, bestScores.Scores, "result_omit_001", "time_ms", 9999)
}
