package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// CISA's Known Exploited Vulnerabilities catalog: what attackers are using in the wild. Matching
// the lab against it separates the few vulnerabilities that matter now from the many that don't.
const (
	kevURL      = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	kevMaxAge   = 20 * time.Hour
	kevMaxBytes = 32 << 20
	kevFile     = "lab-kev.json"
)

// knownExploited returns the catalog's CVE ids, fetched at most daily and kept in the cache
// directory. When CISA can't be reached a stale copy is used; with none, the error.
func (s *Service) knownExploited(ctx context.Context) (map[string]bool, error) {
	path := filepath.Join(s.cacheDir, kevFile)
	cached, at, cacheErr := readKEV(path)
	if cacheErr == nil && s.now().Sub(at) < kevMaxAge {
		return cached, nil
	}
	ids, err := s.fetchKEV(ctx)
	if err != nil {
		if cacheErr == nil {
			s.log.Warn("lab: can't fetch CISA's exploited list; using the copy from before", "fetched", at, "error", err)
			return cached, nil
		}
		return nil, err
	}
	if b, err := json.Marshal(ids); err == nil {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return toSet(ids), nil
}

func (s *Service) fetchKEV(ctx context.Context) ([]string, error) {
	url := s.kevURL
	if url == "" {
		url = kevURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("CISA's exploited list: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CISA's exploited list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CISA's exploited list: HTTP %d", resp.StatusCode)
	}
	var feed struct {
		Vulnerabilities []struct {
			CVEID string `json:"cveID"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, kevMaxBytes)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("CISA's exploited list: %w", err)
	}
	if len(feed.Vulnerabilities) == 0 {
		return nil, errors.New("CISA's exploited list was empty")
	}
	ids := make([]string, 0, len(feed.Vulnerabilities))
	for _, v := range feed.Vulnerabilities {
		ids = append(ids, v.CVEID)
	}
	return ids, nil
}

// readKEV reads the cached list and when it was fetched.
func readKEV(path string) (map[string]bool, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err //nolint:wrapcheck // only "is there a usable copy" matters
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err //nolint:wrapcheck // as above
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil || len(ids) == 0 {
		return nil, time.Time{}, errors.New("the cached exploited list is unreadable")
	}
	return toSet(ids), info.ModTime(), nil
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
