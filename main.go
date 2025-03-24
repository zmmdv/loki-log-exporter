package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Config holds the application configuration
type Config struct {
	LokiURL        string
	QueryFilePath  string
	Query          string
	SlackToken     string
	ChannelID      string
	PollSeconds    int
	VerboseLogging bool
}

// LokiResponse represents the structure of Loki's API response
type LokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// LokiLabelsResponse represents the structure of Loki's label response
type LokiLabelsResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
}

// Create a struct to track sent messages with timestamps
type SentMessage struct {
	Content   string
	RequestID string
	Timestamp time.Time
}

// trackSentMessages keeps track of messages sent to avoid duplicates
var sentMessages = make(map[string]SentMessage)
var sentRequestIDs = make(map[string]time.Time)

// SlackHistoryResponse represents the structure of Slack's conversation history API response
type SlackHistoryResponse struct {
	Ok       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Messages []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Ts      string `json:"ts"`
		User    string `json:"user,omitempty"`
		BotID   string `json:"bot_id,omitempty"`
	} `json:"messages"`
	HasMore bool `json:"has_more"`
}

// loadConfigFromEnv loads configuration from environment variables
func loadConfigFromEnv() (*Config, error) {
	config := &Config{
		LokiURL:        getEnv("LOKI_URL", "http://localhost:3100"),
		QueryFilePath:  getEnv("QUERY_FILE", "/data/query"),
		SlackToken:     os.Getenv("SLACK_TOKEN"),
		ChannelID:      os.Getenv("CHANNEL_ID"),
		PollSeconds:    1, // Default to 1 second
		VerboseLogging: getEnvBool("VERBOSE_LOGGING", true),
	}

	// Read query from file
	content, err := ioutil.ReadFile(config.QueryFilePath)
	if err != nil {
		return nil, fmt.Errorf("error reading query file %s: %v", config.QueryFilePath, err)
	}
	config.Query = string(content)

	// Validate required fields
	if config.SlackToken == "" {
		return nil, fmt.Errorf("SLACK_TOKEN environment variable is required")
	}
	if config.ChannelID == "" {
		return nil, fmt.Errorf("CHANNEL_ID environment variable is required")
	}
	if config.Query == "" {
		return nil, fmt.Errorf("query file at %s is empty", config.QueryFilePath)
	}

	return config, nil
}

// getEnv gets an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// getEnvBool gets a boolean environment variable or returns a default value
func getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return strings.ToLower(value) == "true" || value == "1"
}

// queryLoki sends a query to Loki and returns the results
func queryLoki(config *Config) (*LokiResponse, error) {
	// Construct the Loki query URL with appropriate time range (last 1 hour)
	now := time.Now().Unix()
	oneHourAgo := now - 3600 // 1 hour = 3600 seconds
	
	// URL encode the query
	encodedQuery := url.QueryEscape(config.Query)
	
	queryURL := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=1000", 
		config.LokiURL, 
		encodedQuery,
		oneHourAgo,
		now)

	if config.VerboseLogging {
		log.Printf("Querying Loki with URL: %s", queryURL)
		log.Printf("Original query: %s", config.Query)
		log.Printf("Time window: last hour (%d seconds)", now - oneHourAgo)
	}

	// Send the HTTP request
	resp, err := http.Get(queryURL)
	if err != nil {
		return nil, fmt.Errorf("error querying Loki: %v", err)
	}
	defer resp.Body.Close()

	// Read and parse the response
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading Loki response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Log the full response for debugging
		log.Printf("Full Loki error response: %s", string(body))
		return nil, fmt.Errorf("Loki returned non-OK status: %d, body: %s", resp.StatusCode, string(body))
	}

	var lokiResp LokiResponse
	if err := json.Unmarshal(body, &lokiResp); err != nil {
		return nil, fmt.Errorf("error parsing Loki response: %v", err)
	}

	return &lokiResp, nil
}

// extractRequestID extracts the X-Request-ID from a log line
func extractRequestID(logLine string) string {
	// Use regex to find X-Request-ID pattern
	re := regexp.MustCompile(`X-Request-ID:\s+([a-zA-Z0-9]+)`)
	matches := re.FindStringSubmatch(logLine)
	
	if len(matches) > 1 {
		requestID := matches[1]
		return requestID
	}
	
	// If no X-Request-ID found, return empty string
	return ""
}

// checkSlackHistory checks if a message with the same X-Request-ID was already sent to the channel
func checkSlackHistory(config *Config, logLine string) (bool, error) {
	// Extract X-Request-ID from the log line
	requestID := extractRequestID(logLine)
	
	// If no request ID found, fall back to content-based deduplication
	if requestID == "" {
		if config.VerboseLogging {
			log.Printf("No X-Request-ID found in log, falling back to content comparison")
		}
		return checkSlackHistoryByContent(config, logLine)
	}
	
	// Get history from the last 24h (86400 seconds)
	now := time.Now().Unix()
	oldest := now - 86400

	// Construct the Slack API URL for conversation history
	historyURL := fmt.Sprintf("https://slack.com/api/conversations.history?channel=%s&limit=100&oldest=%d", 
		config.ChannelID, oldest)

	req, err := http.NewRequest("GET", historyURL, nil)
	if err != nil {
		return false, fmt.Errorf("error creating Slack history request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+config.SlackToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error getting Slack history: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("error reading Slack history response: %v", err)
	}

	var historyResp SlackHistoryResponse
	if err := json.Unmarshal(body, &historyResp); err != nil {
		return false, fmt.Errorf("error parsing Slack history response: %v", err)
	}

	if !historyResp.Ok {
		return false, fmt.Errorf("Slack API error: %s", historyResp.Error)
	}

	// Check for the X-Request-ID in each message
	requestIDPattern := fmt.Sprintf("X-Request-ID: %s", requestID)
	
	for _, msg := range historyResp.Messages {
		if strings.Contains(msg.Text, requestIDPattern) {
			if config.VerboseLogging {
				log.Printf("Message with X-Request-ID %s already found in Slack channel", requestID)
			}
			return true, nil
		}
	}

	if config.VerboseLogging {
		log.Printf("No message with X-Request-ID %s found in Slack channel", requestID)
	}
	return false, nil
}

// checkSlackHistoryByContent checks if exact message content exists in Slack history
func checkSlackHistoryByContent(config *Config, logLine string) (bool, error) {
	// Get history from the last 24h (86400 seconds)
	now := time.Now().Unix()
	oldest := now - 86400

	// Construct the Slack API URL for conversation history
	historyURL := fmt.Sprintf("https://slack.com/api/conversations.history?channel=%s&limit=100&oldest=%d", 
		config.ChannelID, oldest)

	req, err := http.NewRequest("GET", historyURL, nil)
	if err != nil {
		return false, fmt.Errorf("error creating Slack history request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+config.SlackToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error getting Slack history: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("error reading Slack history response: %v", err)
	}

	var historyResp SlackHistoryResponse
	if err := json.Unmarshal(body, &historyResp); err != nil {
		return false, fmt.Errorf("error parsing Slack history response: %v", err)
	}

	if !historyResp.Ok {
		return false, fmt.Errorf("Slack API error: %s", historyResp.Error)
	}

	// The log message with code formatting as it would appear in Slack
	formattedLog := "```" + logLine + "```"

	// Check if our message is in the history by comparing content
	for _, msg := range historyResp.Messages {
		if msg.Text == formattedLog {
			if config.VerboseLogging {
				log.Printf("Exact message content already found in Slack channel")
			}
			return true, nil
		}
	}

	return false, nil
}

// sendToSlack sends a message to Slack
func sendToSlack(config *Config, message string) error {
	// Prepare the Slack API request payload
	payload := map[string]interface{}{
		"channel": config.ChannelID,
		"text":    message,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshalling Slack payload: %v", err)
	}

	// Create the HTTP request
	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating Slack request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.SlackToken)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending message to Slack: %v", err)
	}
	defer resp.Body.Close()

	// Check the response
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("Slack API returned error: %s", body)
	}

	return nil
}

// testLokiConnection tests connectivity to Loki with a simple query
func testLokiConnection(lokiURL string) error {
	now := time.Now().Unix()
	fiveSecondsAgo := now - 5
	testURL := fmt.Sprintf("%s/loki/api/v1/query_range?query={app=~\".+\"}&start=%d&end=%d&limit=1", 
		lokiURL,
		fiveSecondsAgo,
		now)
	
	log.Printf("Testing Loki connectivity with: %s", testURL)
	
	resp, err := http.Get(testURL)
	if err != nil {
		return fmt.Errorf("connection test failed: %v", err)
	}
	defer resp.Body.Close()
	
	body, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Loki returned status %d: %s", resp.StatusCode, string(body))
	}
	
	log.Printf("Successfully connected to Loki server")
	return nil
}

// DiscoverLokiJobs discovers available jobs in Loki
func DiscoverLokiJobs(lokiURL string) error {
	// Get available labels first
	labelsURL := fmt.Sprintf("%s/loki/api/v1/labels", lokiURL)
	log.Printf("Discovering Loki labels: %s", labelsURL)
	
	resp, err := http.Get(labelsURL)
	if err != nil {
		return fmt.Errorf("error getting Loki labels: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("Loki returned status %d for labels: %s", resp.StatusCode, string(body))
	}
	
	var labelResp LokiLabelsResponse
	body, _ := ioutil.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &labelResp); err != nil {
		return fmt.Errorf("error parsing Loki labels response: %v", err)
	}
	
	log.Printf("Available Loki labels: %v", labelResp.Data)
	
	// Check if job is among the labels
	jobFound := false
	for _, label := range labelResp.Data {
		if label == "job" {
			jobFound = true
			break
		}
	}
	
	if !jobFound {
		log.Printf("WARNING: 'job' label not found in Loki. Query may not work correctly.")
		return nil
	}
	
	// Get values for job label
	jobsURL := fmt.Sprintf("%s/loki/api/v1/label/job/values", lokiURL)
	log.Printf("Discovering Loki job values: %s", jobsURL)
	
	resp, err = http.Get(jobsURL)
	if err != nil {
		return fmt.Errorf("error getting Loki job values: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("Loki returned status %d for job values: %s", resp.StatusCode, string(body))
	}
	
	var jobsResp LokiLabelsResponse
	body, _ = ioutil.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &jobsResp); err != nil {
		return fmt.Errorf("error parsing Loki jobs response: %v", err)
	}
	
	log.Printf("Available Loki jobs: %v", jobsResp.Data)
	return nil
}

func main() {
	// Load configuration
	config, err := loadConfigFromEnv()
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	log.Printf("Starting Loki to Slack exporter with polling interval of %d seconds", config.PollSeconds)
	log.Printf("Using Loki URL: %s", config.LokiURL)
	log.Printf("Using query file: %s", config.QueryFilePath)
	log.Printf("Query: %s", config.Query)
	log.Printf("Slack channel: %s", config.ChannelID)
	log.Printf("Verbose logging: %v", config.VerboseLogging)
	log.Printf("Querying logs from the past 1 hour")
	
	// Test connection to Loki
	if err := testLokiConnection(config.LokiURL); err != nil {
		log.Printf("WARNING: Initial Loki connection test failed: %v", err)
		log.Printf("Will exit after 3 consecutive failed attempts")
	}
	
	// Discover available jobs in Loki
	if err := DiscoverLokiJobs(config.LokiURL); err != nil {
		log.Printf("WARNING: Failed to discover Loki jobs: %v", err)
		log.Printf("Continuing anyway - discovery is not required for operation")
	}

	// Main polling loop
	ticker := time.NewTicker(time.Duration(config.PollSeconds) * time.Second)
	defer ticker.Stop()

	// Create a separate ticker for connection checks (every 1 second)
	connectionTicker := time.NewTicker(1 * time.Second)
	defer connectionTicker.Stop()

	// Create a ticker for reporting connection stats (every minute)
	reportTicker := time.NewTicker(1 * time.Minute)
	defer reportTicker.Stop()

	// Failed attempts counter
	failedAttempts := 0
	
	// Max allowed consecutive failed attempts
	maxFailedAttempts := 3

	// Connection success counter
	successfulConnections := 0

	// Cleanup old entries from tracking map every hour
	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		// Cleanup old entries from the tracking map
		case <-cleanupTicker.C:
			cleanupSentMessages()
			
		// Report connection stats every minute
		case <-reportTicker.C:
			if successfulConnections > 0 {
				log.Printf("Connection stats: %d successful connections in the last minute", successfulConnections)
				successfulConnections = 0
			}
			
		// Check connection to Loki every second
		case <-connectionTicker.C:
			if err := testLokiConnection(config.LokiURL); err != nil {
				failedAttempts++
				log.Printf("WARNING: Loki connection test failed (%d/%d attempts): %v", 
					failedAttempts, maxFailedAttempts, err)
				
				if failedAttempts >= maxFailedAttempts {
					log.Fatalf("CRITICAL: Failed to connect to Loki backend after %d attempts. Exiting.", maxFailedAttempts)
				}
			} else {
				// Reset counter on successful connection
				if failedAttempts > 0 {
					log.Printf("Loki connection restored after %d failed attempts", failedAttempts)
					failedAttempts = 0
				}
				successfulConnections++
			}
			
		// Normal query polling operation
		case <-ticker.C:
			// Query Loki
			response, err := queryLoki(config)
			
			if err != nil {
				log.Printf("Error querying Loki: %v", err)
				continue
			}

			// Process results
			resultsFound := 0
			for _, result := range response.Data.Result {
				for _, value := range result.Values {
					// The log line is in the second element of the value array
					logLine := value[1]
					resultsFound++
					
					// Print what was found
					if config.VerboseLogging {
						log.Printf("Found log entry: %s", strings.Split(logLine, "\n")[0])
					}
					
					// Extract X-Request-ID for deduplication
					requestID := extractRequestID(logLine)
					
					// If we have a valid request ID and we've already sent it, skip
					if requestID != "" {
						if _, exists := sentRequestIDs[requestID]; exists {
							if config.VerboseLogging {
								log.Printf("Skipping duplicate log entry with X-Request-ID %s (found in local cache)", requestID)
							}
							continue
						}
					} else {
						// Fall back to content-based deduplication for the local cache
						if _, exists := sentMessages[logLine]; exists {
							if config.VerboseLogging {
								log.Printf("Skipping duplicate log entry (found in local cache)")
							}
							continue
						}
					}
					
					// Check if this message was already sent to Slack
					exists, err := checkSlackHistory(config, logLine)
					if err != nil {
						log.Printf("Error checking Slack history: %v", err)
					} else if exists {
						// Message already in Slack, don't send again but add to local cache
						if requestID != "" {
							sentRequestIDs[requestID] = time.Now()
							if config.VerboseLogging {
								log.Printf("Adding X-Request-ID %s to local cache (found in Slack)", requestID)
							}
						}
						sentMessages[logLine] = SentMessage{
							Content:   logLine,
							RequestID: requestID,
							Timestamp: time.Now(),
						}
						if config.VerboseLogging {
							log.Printf("Skipping duplicate log entry (found in Slack channel)")
						}
						continue
					}
					
					// Mark this message as sent
					sentMessages[logLine] = SentMessage{
						Content:   logLine,
						RequestID: requestID,
						Timestamp: time.Now(),
					}
					
					// If it has a request ID, also track it separately
					if requestID != "" {
						sentRequestIDs[requestID] = time.Now()
						if config.VerboseLogging {
							log.Printf("Tracking X-Request-ID: %s", requestID)
						}
					}
					
					// Send to Slack
					log.Printf("Sending alert to Slack: %s", strings.Split(logLine, "\n")[0])
					if err := sendToSlack(config, "```"+logLine+"```"); err != nil {
						log.Printf("Error sending to Slack: %v", err)
					} else {
						if requestID != "" {
							log.Printf("Alert successfully sent to Slack with X-Request-ID = %s", requestID)
						} else {
							log.Printf("Alert successfully sent to Slack")
						}
					}
					
					// If we have too many entries, clean up older ones
					if len(sentMessages) > 10000 || len(sentRequestIDs) > 10000 {
						cleanupSentMessages()
					}
				}
			}
			
			if config.VerboseLogging {
				if resultsFound == 0 {
					log.Printf("No matching logs found in this poll")
				} else {
					log.Printf("Found %d log entries in this poll", resultsFound)
				}
			}
		}
	}
}

// getLogHash creates a unique identifier for a log entry
func getLogHash(logLine string) string {
	// A simple hash implementation that should be good enough for deduplication
	return fmt.Sprintf("%x", md5.Sum([]byte(logLine)))
}

// cleanupSentMessages removes old entries from the tracking map
func cleanupSentMessages() {
	now := time.Now()
	// Keep messages from the last day only
	cutoff := now.Add(-24 * time.Hour)
	
	count := 0
	for key, msg := range sentMessages {
		if msg.Timestamp.Before(cutoff) {
			delete(sentMessages, key)
			count++
		}
	}
	
	// Also clean up request IDs map
	reqIDCount := 0
	for key, timestamp := range sentRequestIDs {
		if timestamp.Before(cutoff) {
			delete(sentRequestIDs, key)
			reqIDCount++
		}
	}
	
	if count > 0 || reqIDCount > 0 {
		log.Printf("Cleaned up %d old entries from message tracking cache and %d old request IDs", count, reqIDCount)
	}
} 