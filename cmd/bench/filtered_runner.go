package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	randv2 "math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

type filterCase struct {
	ID        string  `json:"filter_id"`
	Cutoff    *int    `json:"publication_year_gte"`
	TargetPct int     `json:"target_selectivity_pct"`
	Matching  int64   `json:"matching_count"`
	Actual    float64 `json:"actual_selectivity"`
}

type skippedFilter struct {
	TargetPct int    `json:"target_selectivity_pct"`
	Reason    string `json:"reason"`
}

type filteredSample struct {
	Iteration     int     `json:"iteration"`
	LatencyMS     float64 `json:"latency_ms"`
	Recall        float64 `json:"recall_at_k"`
	ReturnedCount int     `json:"returned_count"`
	ResultIDs     []int64 `json:"result_ids"`
}

type filteredQuery struct {
	Filter           filterCase       `json:"filter"`
	QueryID          string           `json:"query_id"`
	QuerySHA256      string           `json:"query_sha256"`
	K                int              `json:"k"`
	Method           string           `json:"method"`
	Parameters       json.RawMessage  `json:"search_parameters"`
	PostgresSettings json.RawMessage  `json:"postgres_settings"`
	Recall           float64          `json:"mean_recall_at_k"`
	P50MS            float64          `json:"p50_ms"`
	P95MS            float64          `json:"p95_ms"`
	P99MS            float64          `json:"p99_ms"`
	ReturnedCount    int              `json:"returned_count"`
	ReturnedCountMin int              `json:"returned_count_min"`
	ReturnedCountMax int              `json:"returned_count_max"`
	TooFewSamples    int              `json:"too_few_samples"`
	PlanIndexName    string           `json:"plan_index_name"`
	Plan             json.RawMessage  `json:"plan_json"`
	Samples          []filteredSample `json:"samples"`
}

type filteredReport struct {
	RunID              string          `json:"run_id"`
	ExperimentID       string          `json:"experiment_id"`
	ConfigVersion      int             `json:"config_version"`
	ConfigSHA256       string          `json:"config_sha256"`
	GitCommit          string          `json:"git_commit"`
	GitDirty           bool            `json:"git_dirty"`
	PostgresVersion    string          `json:"postgres_version"`
	PgvectorVersion    string          `json:"pgvector_version"`
	DatasetSnapshot    string          `json:"dataset_snapshot"`
	DatasetSHA256      string          `json:"dataset_sha256"`
	Papers             int64           `json:"paper_count"`
	EligibleVectors    int64           `json:"eligible_count"`
	ModelID            string          `json:"model_id"`
	ModelRevision      string          `json:"model_revision"`
	TextVersion        string          `json:"text_version"`
	QuerySetVersion    string          `json:"query_set_version"`
	QuerySetSHA256     string          `json:"query_set_sha256"`
	Seed               int64           `json:"seed"`
	WarmupIterations   int             `json:"warmup_iterations"`
	MeasuredIterations int             `json:"measured_iterations"`
	HNSWIndexOptions   []string        `json:"hnsw_index_options"`
	IVFBuildID         string          `json:"ivfflat_build_id"`
	IVFIndexOptions    []string        `json:"ivfflat_index_options"`
	IVFBuildSettings   json.RawMessage `json:"ivfflat_build_settings"`
	IVFBuildMS         float64         `json:"ivfflat_build_ms"`
	IVFBuildEligible   int64           `json:"ivfflat_build_eligible_count"`
	PlanMode           string          `json:"plan_mode"`
	Skipped            []skippedFilter `json:"skipped_filters"`
	Percentile         string          `json:"percentile_definition"`
	CreatedAt          time.Time       `json:"created_at"`
	Queries            []filteredQuery `json:"queries"`
}

func filtersForDataset(ctx context.Context, pool *pgxpool.Pool, eligible int64, targets []int, minResults int) ([]filterCase, []skippedFilter, error) {
	if eligible == 0 {
		return nil, nil, errors.New("no eligible embeddings")
	}
	rows, err := pool.Query(ctx, `SELECT publication_year, count(*) FROM public.papers
		WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
		AND embedding_model=$1 AND embedding_revision=$2 AND embedding_text_version=$3
		GROUP BY publication_year ORDER BY publication_year DESC NULLS LAST`,
		search.ModelID, search.ModelRevision, search.TextVersion)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var years []yearCount
	for rows.Next() {
		var year *int
		var count int64
		if err := rows.Scan(&year, &count); err != nil {
			return nil, nil, err
		}
		if year == nil {
			continue
		}
		years = append(years, yearCount{*year, count})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	cases, skipped := selectFilterCases(eligible, years, targets, minResults)
	return cases, skipped, nil
}

type yearCount struct {
	Year  int
	Count int64
}

// years are ordered newest first, exactly like the SQL query above.
func selectFilterCases(eligible int64, years []yearCount, targets []int, minResults int) ([]filterCase, []skippedFilter) {
	cases := []filterCase{{ID: "all", TargetPct: 100, Matching: eligible, Actual: 1}}
	var candidates []filterCase
	var cumulative int64
	for _, year := range years {
		cumulative += year.Count
		value := year.Year
		candidates = append(candidates, filterCase{ID: fmt.Sprintf("year_gte_%d", value), Cutoff: &value,
			Matching: cumulative, Actual: float64(cumulative) / float64(eligible)})
	}
	used := map[string]bool{"all": true}
	var skipped []skippedFilter
	for _, target := range targets[1:] {
		var best filterCase
		difference := math.Inf(1)
		for _, candidate := range candidates {
			if used[candidate.ID] || candidate.Matching < int64(minResults) {
				continue
			}
			d := math.Abs(candidate.Actual - float64(target)/100)
			if d < difference {
				difference, best = d, candidate
			}
		}
		// A point that is much farther than the intended stratum is not a
		// useful stand-in; state explicitly that this corpus lacks the filter.
		tolerance := math.Max(0.005, float64(target)/100*0.25)
		if best.ID == "" || difference > tolerance {
			skipped = append(skipped, skippedFilter{target, "no distinct year cutoff with enough matches near target"})
			continue
		}
		best.TargetPct = target
		cases = append(cases, best)
		used[best.ID] = true
	}
	return cases, skipped
}

type filteredTask struct {
	index int
	q     fixedQuery
}

func filteredKey(f filterCase, q fixedQuery, k int) string {
	return fmt.Sprintf("%s/%s/%d", f.ID, q.ID, k)
}

func executeFiltered(ctx context.Context, pool *pgxpool.Pool, cfg filteredConfig, cfgSHA, fixtureVersion, fixtureSHA string, queries []fixedQuery) (*filteredReport, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	commit, dirty, err := gitState()
	if err != nil {
		return nil, err
	}
	r := &filteredReport{RunID: id, ExperimentID: cfg.ExperimentID, ConfigVersion: cfg.SchemaVersion,
		ConfigSHA256: cfgSHA, GitCommit: commit, GitDirty: dirty, DatasetSnapshot: cfg.DatasetSnapshot,
		ModelID: search.ModelID, ModelRevision: search.ModelRevision, TextVersion: search.TextVersion,
		QuerySetVersion: fixtureVersion, QuerySetSHA256: fixtureSHA, Seed: cfg.Seed,
		WarmupIterations: cfg.WarmupIterations, MeasuredIterations: cfg.MeasuredIterations,
		PlanMode: "planner", Percentile: percentileDefinition, CreatedAt: time.Now().UTC()}
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version'),
		(SELECT extversion FROM pg_extension WHERE extname='vector')`).Scan(&r.PostgresVersion, &r.PgvectorVersion); err != nil {
		return nil, err
	}
	initial, err := search.SnapshotWithYears(ctx, pool)
	if err != nil {
		return nil, err
	}
	r.Papers, r.EligibleVectors, r.DatasetSHA256 = initial.Papers, initial.Eligible, initial.SHA256
	if initial.Eligible == 0 {
		return nil, errors.New("no eligible embeddings")
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT c.reloptions,i.indisvalid FROM pg_class c
		JOIN pg_index i ON i.indexrelid=c.oid
		WHERE c.oid=to_regclass('public.papers_embedding_ivfflat_cosine_idx')`).Scan(&r.IVFIndexOptions, &valid); err != nil || !valid {
		return nil, fmt.Errorf("build a valid IVFFlat index with cmd/bench-index first: %v", err)
	}
	if !slices.Contains(r.IVFIndexOptions, fmt.Sprintf("lists=%d", cfg.IVFLists)) {
		return nil, errors.New("IVFFlat index lists do not match experiment config")
	}
	if err := pool.QueryRow(ctx, `SELECT reloptions FROM pg_class WHERE oid='public.papers_embedding_hnsw_cosine_idx'::regclass`).Scan(&r.HNSWIndexOptions); err != nil {
		return nil, err
	}
	var buildSHA string
	if err := pool.QueryRow(ctx, `SELECT build_id,eligible_count,dataset_sha256,build_ms,build_settings
		FROM public.bench_index_builds WHERE index_name=$1 ORDER BY created_at DESC LIMIT 1`,
		search.IVFFlatIndexName).Scan(&r.IVFBuildID, &r.IVFBuildEligible, &buildSHA, &r.IVFBuildMS, &r.IVFBuildSettings); err != nil {
		return nil, fmt.Errorf("index build record missing: %w", err)
	}
	if buildSHA != initial.SHA256 || r.IVFBuildEligible != initial.Eligible {
		return nil, errors.New("corpus changed since IVFFlat training; rebuild the index for this snapshot")
	}
	filters, skipped, err := filtersForDataset(ctx, pool, initial.Eligible, cfg.TargetPercentages, 20)
	if err != nil {
		return nil, err
	}
	r.Skipped = skipped
	opts := search.FilterOptions{EFSearch: cfg.HNSWEFSearch, Probes: cfg.IVFProbes,
		MaxProbes: cfg.IVFMaxProbes, IterativeScan: cfg.IterativeScan}
	ground := map[string][]search.Result{}
	for _, f := range filters {
		for _, q := range queries {
			for _, k := range cfg.K {
				got, err := search.Filtered(ctx, pool, "exact", q.Vector, k, f.Cutoff, opts)
				if err != nil {
					return nil, err
				}
				if len(got) == 0 {
					return nil, fmt.Errorf("no exact results for %s/%s", f.ID, q.ID)
				}
				ground[filteredKey(f, q, k)] = got
			}
		}
	}
	var tasks []filteredTask
	for _, f := range filters {
		for _, q := range queries {
			for _, k := range cfg.K {
				for _, method := range []string{"exact", "hnsw", "ivfflat"} {
					params := map[string]any{"iterative_scan": cfg.IterativeScan}
					switch method {
					case "exact":
						params = map[string]any{}
					case "hnsw":
						params["ef_search"] = cfg.HNSWEFSearch
					case "ivfflat":
						params["lists"] = cfg.IVFLists
						params["probes"] = cfg.IVFProbes
						params["max_probes"] = cfg.IVFMaxProbes
					}
					encoded, _ := json.Marshal(params)
					r.Queries = append(r.Queries, filteredQuery{Filter: f, QueryID: q.ID, QuerySHA256: q.SHA256, K: k, Method: method, Parameters: encoded})
					tasks = append(tasks, filteredTask{len(r.Queries) - 1, q})
				}
			}
		}
	}
	rng := randv2.New(randv2.NewPCG(uint64(cfg.Seed), 0x5eed))
	for iteration := -cfg.WarmupIterations; iteration < cfg.MeasuredIterations; iteration++ {
		order := slices.Clone(tasks)
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, task := range order {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row := &r.Queries[task.index]
			start := time.Now()
			got, err := search.Filtered(ctx, pool, row.Method, task.q.Vector, row.K, row.Filter.Cutoff, opts)
			ms := float64(time.Since(start).Nanoseconds()) / 1e6
			if err != nil {
				return nil, err
			}
			if iteration < 0 {
				continue
			}
			expected := ground[filteredKey(row.Filter, task.q, row.K)]
			recall, ok := search.RecallAtK(expected, got, row.K)
			if !ok || (row.Method == "exact" && !slices.Equal(ids(expected), ids(got))) {
				return nil, fmt.Errorf("exact filtered ground truth changed for %s", row.Filter.ID)
			}
			row.Samples = append(row.Samples, filteredSample{iteration, ms, recall, len(got), ids(got)})
		}
	}
	for i := range r.Queries {
		row := &r.Queries[i]
		if err := summarizeFiltered(row); err != nil {
			return nil, err
		}
		q := queryByID(queries, row.QueryID)
		plan, settings, err := search.FilteredPlan(ctx, pool, row.Method, q.Vector, row.K, row.Filter.Cutoff, opts)
		if err != nil {
			return nil, err
		}
		indexes := planIndexes(plan)
		if row.Method == "exact" && (slices.Contains(indexes, search.HNSWIndexName) || slices.Contains(indexes, search.IVFFlatIndexName)) {
			return nil, errors.New("exact filtered plan used an ANN index")
		}
		if row.Method == "hnsw" && slices.Contains(indexes, search.IVFFlatIndexName) ||
			row.Method == "ivfflat" && slices.Contains(indexes, search.HNSWIndexName) {
			return nil, errors.New("ANN query used the other method's index")
		}
		row.PlanIndexName = strings.Join(indexes, ",")
		row.Plan, err = redactPlan(plan)
		if err != nil {
			return nil, err
		}
		row.PostgresSettings = settings
	}
	final, err := search.SnapshotWithYears(ctx, pool)
	if err != nil {
		return nil, err
	}
	if final != initial {
		return nil, errors.New("dataset changed during filtered experiment; results discarded")
	}
	return r, nil
}

func summarizeFiltered(row *filteredQuery) error {
	if len(row.Samples) == 0 {
		return errors.New("no measured samples")
	}
	times := make([]float64, 0, len(row.Samples))
	row.ReturnedCountMin = row.K
	for _, s := range row.Samples {
		times = append(times, s.LatencyMS)
		row.Recall += s.Recall
		row.ReturnedCountMin = min(row.ReturnedCountMin, s.ReturnedCount)
		row.ReturnedCountMax = max(row.ReturnedCountMax, s.ReturnedCount)
		if s.ReturnedCount < min(row.K, int(row.Filter.Matching)) {
			row.TooFewSamples++
		}
	}
	row.Recall /= float64(len(row.Samples))
	row.ReturnedCount = row.Samples[0].ReturnedCount
	var err error
	if row.P50MS, err = percentile(times, 50); err != nil {
		return err
	}
	if row.P95MS, err = percentile(times, 95); err != nil {
		return err
	}
	row.P99MS, err = percentile(times, 99)
	return err
}

func planIndexes(raw json.RawMessage) []string {
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	found := map[string]bool{}
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			if name, ok := value["Index Name"].(string); ok {
				found[name] = true
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(parsed)
	var names []string
	for name := range found {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
