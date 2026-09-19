//go:build integration

package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// Integration test configuration
type IntegrationConfig struct {
	DBConnString    string
	SolutionSalt    string
	TestAPIKey      string
	TestAPIKey2     string
	TestUserID      string
	TestUserID2     string
	TestFriendCode  string
	TestFriendCode2 string
}

// Global integration test configuration
var integrationConfig IntegrationConfig

// Integration test utilities

func connectToIntegrationDB() (*sql.DB, error) {
	return sql.Open("postgres", integrationConfig.DBConnString)
}

func prepareIntegrationDatabase() {
	db, err := connectToIntegrationDB()
	if err != nil {
		log.Fatalf("Failed to connect to integration database: %v", err)
	}
	defer db.Close()

	// Reset schema from migration files for long-term compatibility as migrations grow.
	log.Println("Rolling database schema down via migrations...")
	if err := applyMigrationsDownAll(db, "../sql/migrations"); err != nil {
		log.Printf("Warning: Failed to apply down migrations: %v", err)
	}

	// Apply database migrations for integration tests
	if err := applyMigrationsUp(db, "../sql/migrations"); err != nil {
		log.Fatalf("Failed to apply database migrations: %v", err)
	}

	// Ensure dev user's API key hash is in sync for admin-related flows.
	if err := syncDevAPIKeyHash(db); err != nil {
		log.Fatalf("Failed to sync dev API key hash: %v", err)
	}

	// Clean up existing test data
	cleanupIntegrationData()
}

func cleanupIntegrationData() {
	db, err := connectToIntegrationDB()
	if err != nil {
		log.Printf("Warning: Failed to connect for cleanup: %v", err)
		return
	}
	defer db.Close()

	// Delete all user-created data (preserve system users and score types)
	// Order matters due to foreign key constraints
	queries := []string{
		// Delete scores first (references solution)
		"DELETE FROM score WHERE solution_id IN (SELECT id FROM solution WHERE user_id NOT IN ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001'))",
		// Delete solutions (references user)
		"DELETE FROM solution WHERE user_id NOT IN ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001')",
		// Delete user relations (references user)
		"DELETE FROM user_relation WHERE user_id_source NOT IN ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001') OR user_id_target NOT IN ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001')",
		// Delete test users (keep system users)
		"DELETE FROM \"user\" WHERE id NOT IN ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001')",
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			log.Printf("Warning: Failed to clean up test data: %v", err)
		}
	}
}

func createIntegrationTestUsers() {
	// Create test server for user creation
	db := mustConnectToIntegrationDB()
	defer db.Close()

	server := httptest.NewServer(setupTestRoutes(db))
	defer server.Close()

	// Create two test users
	user1, err := createIntegrationTestUser(server.URL)
	if err != nil {
		log.Fatalf("Failed to create integration test user 1: %v", err)
	}
	integrationConfig.TestAPIKey = user1.APIKey
	integrationConfig.TestUserID = user1.ID
	integrationConfig.TestFriendCode = user1.FriendCode

	user2, err := createIntegrationTestUser(server.URL)
	if err != nil {
		log.Fatalf("Failed to create integration test user 2: %v", err)
	}
	integrationConfig.TestAPIKey2 = user2.APIKey
	integrationConfig.TestUserID2 = user2.ID
	integrationConfig.TestFriendCode2 = user2.FriendCode
}

func mustConnectToIntegrationDB() *sql.DB {
	db, err := connectToIntegrationDB()
	if err != nil {
		log.Fatalf("Failed to connect to integration database: %v", err)
	}
	return db
}

// Test user type for integration tests
type IntegrationTestUser struct {
	ID          string `json:"id"`
	FriendCode  string `json:"friend_code"`
	DisplayName string `json:"display_name"`
	APIKey      string `json:"api_key"`
}

func createIntegrationTestUser(baseURL string) (*IntegrationTestUser, error) {
	// Create request body for new user creation
	requestBody := map[string]string{
		"game_id":      "com.company.testgame",
		"game_version": "1.0.0",
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := http.Post(baseURL+APIBasePath+"/user", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var user IntegrationTestUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

func authenticateIntegrationTestUser(baseURL string, userID string, apiKey string) (*IntegrationTestUser, error) {
	// Create request body for user authentication
	requestBody := map[string]string{
		"game_id":      "com.company.testgame",
		"game_version": "1.0.0",
		"user_id":      userID,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request with Authorization header
	req, err := http.NewRequest("POST", baseURL+APIBasePath+"/user", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("authentication failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var user IntegrationTestUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

// loginWithUserIDAndAPIKey authenticates or creates a user with provided user ID and apiKey
// The apiKey is passed via Authorization header as Bearer token
func loginWithUserIDAndAPIKey(baseURL string, userID string, apiKey string) (*IntegrationTestUser, int, error) {
	return loginWithUserIDAndAPIKeyAndFriendCode(baseURL, userID, apiKey, "")
}

// loginWithUserIDAndAPIKeyAndFriendCode authenticates or creates a user with
// provided user ID, apiKey, and optional friend code.
func loginWithUserIDAndAPIKeyAndFriendCode(baseURL string, userID string, apiKey string, friendCode string) (*IntegrationTestUser, int, error) {
	requestBody := map[string]interface{}{
		"game_id":      "com.company.testgame",
		"game_version": "1.0.0",
		"user_id":      userID,
	}
	if strings.TrimSpace(friendCode) != "" {
		requestBody["friend_code"] = friendCode
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", baseURL+APIBasePath+"/user", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Add API key to Authorization header if provided
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// For non-success responses, read the body as a string to return error info
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var user IntegrationTestUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, resp.StatusCode, err
	}

	return &user, resp.StatusCode, nil
}

// loginWithUserID creates/authenticates a user with only a user ID (no apiKey in Authorization header)
func loginWithUserID(baseURL string, userID string) (*IntegrationTestUser, int, error) {
	return loginWithUserIDAndAPIKey(baseURL, userID, "")
}

// Integration test HTTP helpers

func makeIntegrationRequest(server *httptest.Server, method, path string, headers map[string]string, body io.Reader) (*http.Response, error) {
	url := server.URL + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{}
	return client.Do(req)
}

func makeIntegrationJSONRequest(server *httptest.Server, method, path string, headers map[string]string, payload interface{}, response interface{}) error {
	var body io.Reader
	if payload != nil {
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal payload: %v", err)
		}
		body = bytes.NewBuffer(jsonData)
	}

	// Set default headers
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/json"

	resp, err := makeIntegrationRequest(server, method, path, headers, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if response != nil {
		return json.NewDecoder(resp.Body).Decode(response)
	}

	return nil
}

func makeAuthenticatedIntegrationRequest(server *httptest.Server, method, path string, apiKey string, payload interface{}, response interface{}) error {
	headers := map[string]string{
		"Authorization": "Bearer " + apiKey,
	}
	return makeIntegrationJSONRequest(server, method, path, headers, payload, response)
}

// Score submission types for integration tests
type IntegrationScoreSubmissionRequest struct {
	Solution     string         `json:"solution"`
	SolutionHash string         `json:"solution_hash"`
	LevelID      string         `json:"level_id"`
	LevelVersion int            `json:"level_version"`
	GameVersion  string         `json:"game_version"`
	Result       string         `json:"result,omitempty"` // abandon, pass, or fail; omit to default to pass
	Scores       map[string]int `json:"scores"`
}

type IntegrationSolution struct {
	Message    string `json:"message,omitempty"`
	SolutionID string `json:"solution_id"`
}

type IntegrationBestScoreEntry struct {
	LevelID      string `json:"level_id"`
	LevelVersion int    `json:"level_version"`
	ScoreType    string `json:"score_type"`
	BestScore    int    `json:"best_score"`
	UserID       string `json:"user_id"`
	DisplayName  string `json:"display_name"`
	FriendCode   string `json:"friend_code"`
}

type IntegrationBestScoresResponse struct {
	Message string                      `json:"message,omitempty"`
	Scores  []IntegrationBestScoreEntry `json:"scores"`
	Count   int                         `json:"count"`
	Scope   string                      `json:"scope"`
}

type IntegrationLeaderboardEntry struct {
	Rank         int    `json:"rank"`
	LevelID      string `json:"level_id"`
	LevelVersion int    `json:"level_version"`
	ScoreType    string `json:"score_type"`
	BestScore    int    `json:"best_score"`
	UserID       string `json:"user_id"`
	DisplayName  string `json:"display_name"`
	FriendCode   string `json:"friend_code"`
}

type IntegrationPaginationInfo struct {
	Offset int `json:"offset"`
	Size   int `json:"size"`
	Total  int `json:"total"`
}

type IntegrationLeaderboardResponse struct {
	Message    string                        `json:"message,omitempty"`
	Scores     []IntegrationLeaderboardEntry `json:"scores"`
	Count      int                           `json:"count"`
	Scope      string                        `json:"scope"`
	Pagination IntegrationPaginationInfo     `json:"pagination"`
}

func submitIntegrationScore(server *httptest.Server, apiKey string, request IntegrationScoreSubmissionRequest) (*IntegrationSolution, error) {
	var response IntegrationSolution
	err := makeAuthenticatedIntegrationRequest(server, "PUT", APIBasePath+"/score", apiKey, request, &response)
	return &response, err
}

func getIntegrationBestScores(server *httptest.Server, apiKey string, levels []string, scope string) (*IntegrationBestScoresResponse, error) {
	// Build query parameters
	params := url.Values{}
	if len(levels) > 0 {
		params.Set("levels", strings.Join(levels, ","))
	}
	if scope != "" {
		params.Set("scope", scope)
	}

	path := APIBasePath + "/score/best"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var response IntegrationBestScoresResponse
	err := makeAuthenticatedIntegrationRequest(server, "GET", path, apiKey, nil, &response)
	return &response, err
}

func getIntegrationLeaderboard(server *httptest.Server, apiKey string, levels []string, scope string, scoreType string, offset int, size int) (*IntegrationLeaderboardResponse, error) {
	// Build query parameters
	params := url.Values{}
	params.Set("levels", strings.Join(levels, ","))
	if scope != "" {
		params.Set("scope", scope)
	}
	if scoreType != "" {
		params.Set("score_type", scoreType)
	}
	params.Set("offset", fmt.Sprintf("%d", offset))
	params.Set("size", fmt.Sprintf("%d", size))

	path := APIBasePath + "/score/leaderboard?" + params.Encode()

	var response IntegrationLeaderboardResponse
	err := makeAuthenticatedIntegrationRequest(server, "GET", path, apiKey, nil, &response)
	return &response, err
}

// Hash calculation utility for integration tests
func calculateIntegrationSolutionHash(solution string) string {
	salted := solution + integrationConfig.SolutionSalt
	hash := sha256.Sum256([]byte(salted))
	return hex.EncodeToString(hash[:])
}

// Test assertion helpers for integration tests
func assertIntegrationScoreExists(t *testing.T, scores []IntegrationBestScoreEntry, levelID string, scoreType string, expectedScore int) {
	for _, score := range scores {
		if score.LevelID == levelID && score.ScoreType == scoreType {
			if score.BestScore != expectedScore {
				t.Errorf("Expected score %d for %s/%s, got %d", expectedScore, levelID, scoreType, score.BestScore)
			}
			return
		}
	}
	t.Errorf("Score not found for %s/%s", levelID, scoreType)
}

func assertIntegrationScoreCount(t *testing.T, response *IntegrationBestScoresResponse, expectedCount int) {
	if response.Count != expectedCount {
		t.Errorf("Expected count %d, got %d", expectedCount, response.Count)
	}
	if len(response.Scores) != expectedCount {
		t.Errorf("Expected %d scores in array, got %d", expectedCount, len(response.Scores))
	}
}

func assertIntegrationScoreFriendCode(t *testing.T, scores []IntegrationBestScoreEntry, levelID string, scoreType string, expectedFriendCode string) {
	for _, score := range scores {
		if score.LevelID == levelID && score.ScoreType == scoreType {
			if score.FriendCode != expectedFriendCode {
				t.Errorf("Expected friend code %s for %s/%s, got %s", expectedFriendCode, levelID, scoreType, score.FriendCode)
			}
			return
		}
	}
	t.Errorf("Score not found for %s/%s", levelID, scoreType)
}
