package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

// Paths in a config are relative to the repository root, not the config file.
type config struct {
	SchemaVersion      int    `json:"schema_version"`
	ExperimentID       string `json:"experiment_id"`
	DatasetSnapshot    string `json:"dataset_snapshot"`
	QuerySet           string `json:"query_set"`
	Seed               int64  `json:"seed"`
	K                  []int  `json:"k"`
	WarmupIterations   int    `json:"warmup_iterations"`
	MeasuredIterations int    `json:"measured_iterations"`
	HNSWEFSearch       int    `json:"hnsw_ef_search"`
	RequireHNSWIndex   bool   `json:"require_hnsw_index"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func decodeStrict(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var tail any
	if err := dec.Decode(&tail); err == nil {
		return errors.New("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func readConfig(path string) (config, string, error) {
	var cfg config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, "", err
	}
	if err := decodeStrict(data, &cfg); err != nil {
		return cfg, "", fmt.Errorf("invalid config JSON: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, "", err
	}
	return cfg, digest(data), nil
}

func (c config) validate() error {
	if c.SchemaVersion != 1 || !identifier.MatchString(c.ExperimentID) || !identifier.MatchString(c.DatasetSnapshot) ||
		c.Seed < 0 || c.WarmupIterations < 0 || c.WarmupIterations > 100 ||
		c.MeasuredIterations < 1 || c.MeasuredIterations > 1000 || len(c.K) == 0 || len(c.K) > 10 {
		return errors.New("invalid experiment config: version, identifiers, seed, iterations or k")
	}
	if c.QuerySet == "" || filepath.IsAbs(c.QuerySet) || filepath.Clean(c.QuerySet) != c.QuerySet ||
		strings.HasPrefix(c.QuerySet, "../") || c.QuerySet == ".." || filepath.Ext(c.QuerySet) != ".json" {
		return errors.New("query_set must be a repository-relative JSON path")
	}
	if err := search.ValidateEFSearch(c.HNSWEFSearch); err != nil {
		return err
	}
	seen := map[int]bool{}
	for _, k := range c.K {
		if err := search.ValidateLimit(k); err != nil {
			return err
		}
		if seen[k] {
			return errors.New("duplicate k")
		}
		seen[k] = true
	}
	return nil
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type queryFile struct {
	ModelID       string    `json:"model_id"`
	ModelRevision string    `json:"model_revision"`
	TextVersion   string    `json:"text_version"`
	Embedding     []float64 `json:"embedding"`
}

type manifest struct {
	Version       string `json:"version"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	TextVersion   string `json:"text_version"`
	Queries       []struct {
		Name string `json:"name"`
		File string `json:"file"`
	} `json:"queries"`
}

type fixedQuery struct {
	ID     string
	SHA256 string
	Vector []float64
}

func loadQueries(path string) (manifest, string, []fixedQuery, error) {
	var m manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, "", nil, err
	}
	if err := decodeStrictManifest(data, &m); err != nil || m.Version == "" || len(m.Queries) == 0 ||
		m.ModelID != search.ModelID || m.ModelRevision != search.ModelRevision || m.TextVersion != search.TextVersion {
		return m, "", nil, fmt.Errorf("invalid query manifest or model version: %v", err)
	}
	seen := map[string]bool{}
	queries := make([]fixedQuery, 0, len(m.Queries))
	for _, entry := range m.Queries {
		if !identifier.MatchString(entry.Name) || seen[entry.Name] ||
			filepath.Base(entry.File) != entry.File || filepath.Ext(entry.File) != ".json" {
			return m, "", nil, errors.New("invalid or duplicate query ID or filename")
		}
		seen[entry.Name] = true
		file, err := os.ReadFile(filepath.Join(filepath.Dir(path), entry.File))
		if err != nil {
			return m, "", nil, err
		}
		var q queryFile
		if err := decodeStrict(file, &q); err != nil {
			return m, "", nil, fmt.Errorf("query %s: %w", entry.Name, err)
		}
		if q.ModelID != search.ModelID || q.ModelRevision != search.ModelRevision || q.TextVersion != search.TextVersion {
			return m, "", nil, fmt.Errorf("query %s: model or preprocessing mismatch", entry.Name)
		}
		if _, err := search.VectorLiteral(q.Embedding); err != nil {
			return m, "", nil, fmt.Errorf("query %s: %w", entry.Name, err)
		}
		queries = append(queries, fixedQuery{entry.Name, digest(file), q.Embedding})
	}
	return m, digest(data), queries, nil
}

// The baseline also includes corpus fixtures and expected results, so it is
// intentionally decoded into a map without rejecting those additional fields.
func decodeStrictManifest(data []byte, m *manifest) error {
	if err := json.Unmarshal(data, m); err != nil {
		return err
	}
	return nil
}

func percentile(samples []float64, p int) (float64, error) {
	if len(samples) == 0 || p < 1 || p > 100 {
		return 0, errors.New("percentile requires samples and p in 1..100")
	}
	values := slices.Clone(samples)
	slices.Sort(values)
	for _, v := range values {
		if v < 0 || v != v || v > 1e300 {
			return 0, errors.New("invalid latency sample")
		}
	}
	// Nearest rank, one-based: ceil(p*N/100), no interpolation.
	index := (p*len(values)+99)/100 - 1
	return values[index], nil
}
