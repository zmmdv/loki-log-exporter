package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
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
	// Construct the Loki query URL with appropriate time range (last 30 seconds)
	now := time.Now().Unix()
	thirtySecondsAgo := now - 30
	
	// URL encode the query
	encodedQuery := url.QueryEscape(config.Query)
	
	queryURL := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=100", 
		config.LokiURL, 
		encodedQuery,
		thirtySecondsAgo,
		now)

	if config.VerboseLogging {
		log.Printf("Querying Loki with URL: %s", queryURL)
		log.Printf("Original query: %s", config.Query)
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

// trackSentMessages keeps track of messages sent to avoid duplicates
var sentMessages = make(map[string]bool)

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
	
	// Test connection to Loki
	if err := testLokiConnection(config.LokiURL); err != nil {
		log.Printf("WARNING: Loki connection test failed: %v", err)
	}
	
	// Discover available jobs in Loki
	if err := DiscoverLokiJobs(config.LokiURL); err != nil {
		log.Printf("WARNING: Failed to discover Loki jobs: %v", err)
	}

	// Main polling loop
	ticker := time.NewTicker(time.Duration(config.PollSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
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
				
				// Check if we've already sent this message
				if sentMessages[logLine] {
					if config.VerboseLogging {
						log.Printf("Skipping duplicate log entry")
					}
					continue
				}
				
				// Mark this message as sent
				sentMessages[logLine] = true
				
				// Send to Slack
				log.Printf("Sending alert to Slack: %s", strings.Split(logLine, "\n")[0])
				if err := sendToSlack(config, "```"+logLine+"```"); err != nil {
					log.Printf("Error sending to Slack: %v", err)
				} else {
					log.Printf("Alert successfully sent to Slack")
				}
				
				// Limit the size of our tracking map to avoid memory leaks
				if len(sentMessages) > 1000 {
					sentMessages = make(map[string]bool)
					log.Printf("Cleared message tracking cache")
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