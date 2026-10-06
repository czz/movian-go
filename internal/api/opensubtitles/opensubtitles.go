package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// OpenSubtitlesClient represents an OpenSubtitles API client
type OpenSubtitlesClient struct {
	userAgent string
	apiKey    string
	token     string
	http      *http.Client
	baseURL   string
}

// NewOpenSubtitlesClient creates a new OpenSubtitles client
func NewOpenSubtitlesClient(userAgent, apiKey string) *OpenSubtitlesClient {
	return &OpenSubtitlesClient{
		userAgent: userAgent,
		apiKey:    apiKey,
		baseURL:   "https://api.opensubtitles.com/api/v1",
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SearchSubtitles searches for subtitles
func (c *OpenSubtitlesClient) SearchSubtitles(ctx context.Context, query string, lang string) ([]Subtitle, error) {
	if c.token == "" {
		return nil, fmt.Errorf("not logged in")
	}

	reqURL := fmt.Sprintf("%s/subtitles", c.baseURL)
	params := url.Values{}
	params.Add("query", query)
	if lang != "" {
		params.Add("languages", lang)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Language      string  `json:"language"`
				DownloadCount int     `json:"download_count"`
				Ratings       float32 `json:"ratings"`
				Files         []struct {
					FileID   int    `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	subtitles := make([]Subtitle, 0, len(result.Data))
	for _, item := range result.Data {
		if len(item.Attributes.Files) > 0 {
			downloadURL := fmt.Sprintf("%s/download/%d", c.baseURL, item.Attributes.Files[0].FileID)
			subtitles = append(subtitles, Subtitle{
				ID:          item.ID,
				Language:    item.Attributes.Language,
				DownloadURL: downloadURL,
				FileName:    item.Attributes.Files[0].FileName,
				Rating:      item.Attributes.Ratings,
				Downloads:   item.Attributes.DownloadCount,
			})
		}
	}

	return subtitles, nil
}

// Subtitle represents a subtitle entry
type Subtitle struct {
	ID          string
	Language    string
	DownloadURL string
	FileName    string
	Rating      float32
	Downloads   int
}

// LoginResponse represents the login response from OpenSubtitles
type LoginResponse struct {
	Token string `json:"token"`
}

// Login authenticates with OpenSubtitles
func (c *OpenSubtitlesClient) Login(ctx context.Context, username, password string) (string, error) {
	reqURL := fmt.Sprintf("%s/login", c.baseURL)

	payload := map[string]string{
		"username": username,
		"password": password,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed: %d", resp.StatusCode)
	}

	var result LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	c.token = result.Token
	return result.Token, nil
}

// Logout logs out from OpenSubtitles
func (c *OpenSubtitlesClient) Logout(ctx context.Context, token string) error {
	reqURL := fmt.Sprintf("%s/logout", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "DELETE", reqURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("logout failed: %d", resp.StatusCode)
	}

	c.token = ""
	return nil
}
