package openalex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	fields           = "id,doi,display_name,publication_year,cited_by_count,abstract_inverted_index,topics,referenced_works"
	maxPageSize      = 100
	maxSearchResults = 10000
	maxResponseBytes = 16 << 20
	maxAttempts      = 3
	maxRetryDelay    = 5 * time.Second
)

type Client struct {
	config Config
	http   *http.Client
}

// New accepts an optional HTTP client, making local HTTP fixtures independent
// of the public API. Request deadlines are applied even to a supplied client.
func New(cfg Config, httpClient *http.Client) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("OPENALEX_TIMEOUT must be a positive duration")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{config: cfg, http: httpClient}, nil
}

// Search returns at most limit works using cursor pagination. It never writes
// to the database. Large bulk exports belong in the OpenAlex snapshot.
func (c *Client) Search(ctx context.Context, search string, limit int) ([]Work, error) {
	if strings.TrimSpace(search) == "" {
		return nil, errors.New("OpenAlex search must not be empty")
	}
	if limit < 1 || limit > maxSearchResults {
		return nil, fmt.Errorf("OpenAlex limit must be between 1 and %d", maxSearchResults)
	}
	works := make([]Work, 0, min(limit, maxPageSize))
	cursor := "*"
	seen := make(map[string]bool)
	for len(works) < limit {
		if seen[cursor] {
			return nil, errors.New("OpenAlex repeated a pagination cursor")
		}
		seen[cursor] = true
		pageSize := min(limit-len(works), maxPageSize)
		page, err := c.fetchPage(ctx, search, cursor, pageSize)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Results {
			work, err := raw.work()
			if err != nil {
				return nil, err
			}
			works = append(works, work)
			if len(works) == limit {
				break
			}
		}
		if len(page.Results) == 0 || page.Meta.NextCursor == nil || *page.Meta.NextCursor == "" {
			break
		}
		cursor = *page.Meta.NextCursor
	}
	return works, nil
}

func (c *Client) fetchPage(ctx context.Context, search, cursor string, pageSize int) (responsePage, error) {
	u, _ := url.Parse(c.config.BaseURL)
	u.Path = "/works"
	query := u.Query()
	query.Set("search", search)
	query.Set("select", fields)
	query.Set("per_page", strconv.Itoa(pageSize))
	query.Set("cursor", cursor)
	u.RawQuery = query.Encode()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		page, status, headers, err := c.request(ctx, u.String())
		if err != nil {
			return responsePage{}, err
		}
		if status == http.StatusOK {
			return page, nil
		}
		if status == http.StatusTooManyRequests && headers.Get("X-RateLimit-Remaining") == "0" {
			return responsePage{}, errors.New("OpenAlex daily request budget exhausted (HTTP 429)")
		}
		if status != http.StatusTooManyRequests && (status < 500 || status > 599) {
			return responsePage{}, fmt.Errorf("OpenAlex returned HTTP %d", status)
		}
		if attempt == maxAttempts-1 {
			return responsePage{}, fmt.Errorf("OpenAlex returned HTTP %d after %d attempts", status, maxAttempts)
		}
		delay := time.Duration(200<<attempt) * time.Millisecond
		if wait, ok := retryAfter(headers.Get("Retry-After")); ok {
			delay = wait
		}
		if delay > maxRetryDelay {
			return responsePage{}, fmt.Errorf("OpenAlex returned HTTP %d; retry delay exceeds %s", status, maxRetryDelay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return responsePage{}, ctx.Err()
		case <-timer.C:
		}
	}
	return responsePage{}, errors.New("OpenAlex retry limit reached")
}

func (c *Client) request(ctx context.Context, address string) (responsePage, int, http.Header, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, address, nil)
	if err != nil {
		return responsePage{}, 0, nil, errors.New("could not prepare OpenAlex request")
	}
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return responsePage{}, 0, nil, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return responsePage{}, 0, nil, fmt.Errorf("OpenAlex request: %w", context.DeadlineExceeded)
		}
		// Do not return a transport error containing a URL or credentials.
		return responsePage{}, 0, nil, errors.New("OpenAlex transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responsePage{}, resp.StatusCode, resp.Header, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return responsePage{}, 0, nil, ctx.Err()
		}
		if requestCtx.Err() != nil {
			return responsePage{}, 0, nil, requestCtx.Err()
		}
		return responsePage{}, 0, nil, errors.New("could not read OpenAlex response")
	}
	if len(data) > maxResponseBytes {
		return responsePage{}, 0, nil, errors.New("OpenAlex response exceeds size limit")
	}
	page, err := decodePage(data)
	return page, resp.StatusCode, resp.Header, err
}

func retryAfter(header string) (time.Duration, bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds >= 0 {
		if seconds > int64(maxRetryDelay/time.Second) {
			return maxRetryDelay + time.Second, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(header); err == nil {
		return max(0, time.Until(date)), true
	}
	return 0, false
}
