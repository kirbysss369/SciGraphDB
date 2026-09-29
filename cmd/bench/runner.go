package main

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	randv2 "math/rand/v2"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kirbysss369/SciGraphDB/internal/search"
)

const percentileDefinition = "nearest-rank: sort ascending; rank=ceil(p*n/100), 1-based; no interpolation; measured samples only"

type sample struct {
	Iteration int     `json:"iteration"`
	LatencyMS float64 `json:"latency_ms"`
	Recall    float64 `json:"recall_at_k"`
	ResultIDs []int64 `json:"result_ids"`
}

type queryResult struct {
	QueryID     string          `json:"query_id"`
	QuerySHA256 string          `json:"query_sha256"`
	K           int             `json:"k"`
	Method      string          `json:"method"`
	Parameters  json.RawMessage `json:"search_parameters"`
	Recall      float64         `json:"mean_recall_at_k"`
	P50MS       float64         `json:"p50_ms"`
	P95MS       float64         `json:"p95_ms"`
	P99MS       float64         `json:"p99_ms"`
	IndexUsed   bool            `json:"index_used"`
	Plan        json.RawMessage `json:"plan_json"`
	Samples     []sample        `json:"samples"`
}

type report struct {
	RunID              string        `json:"run_id"`
	ExperimentID       string        `json:"experiment_id"`
	ConfigVersion      int           `json:"config_version"`
	ConfigSHA256       string        `json:"config_sha256"`
	GitCommit          string        `json:"git_commit"`
	GitDirty           bool          `json:"git_dirty"`
	PostgresVersion    string        `json:"postgres_version"`
	PgvectorVersion    string        `json:"pgvector_version"`
	DatasetSnapshot    string        `json:"dataset_snapshot"`
	DatasetSHA256      string        `json:"dataset_sha256"`
	Papers             int64         `json:"paper_count"`
	EligibleVectors    int64         `json:"eligible_count"`
	ModelID            string        `json:"model_id"`
	ModelRevision      string        `json:"model_revision"`
	TextVersion        string        `json:"text_version"`
	QuerySetVersion    string        `json:"query_set_version"`
	QuerySetSHA256     string        `json:"query_set_sha256"`
	Seed               int64         `json:"seed"`
	WarmupIterations   int           `json:"warmup_iterations"`
	MeasuredIterations int           `json:"measured_iterations"`
	IndexName          string        `json:"index_name"`
	IndexBuildOptions  []string      `json:"index_build_options"`
	IndexBuildMS       *float64      `json:"index_build_ms"`
	IndexBuildNote     string        `json:"index_build_note"`
	Percentile         string        `json:"percentile_definition"`
	CreatedAt          time.Time     `json:"created_at"`
	Queries            []queryResult `json:"queries"`
}

func gitState() (string, bool, error) {
	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, fmt.Errorf("git commit unavailable: %w", err)
	}
	status, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(commit)), len(status) != 0, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Hash rows in ID order, including a hash of the stored vector without ever
// sending the vector itself to the runner or its logs.
func snapshot(ctx context.Context, pool *pgxpool.Pool) (int64, int64, string, error) {
	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.papers`).Scan(&total); err != nil {
		return 0, 0, "", err
	}
	rows, err := pool.Query(ctx, `SELECT id, openalex_id, embedding_text_sha256, md5(embedding::text)
		FROM public.papers WHERE embedding IS NOT NULL AND public.vector_norm(embedding) > 0
		AND embedding_model = $1 AND embedding_revision = $2 AND embedding_text_version = $3 ORDER BY id`,
		search.ModelID, search.ModelRevision, search.TextVersion)
	if err != nil {
		return 0, 0, "", err
	}
	defer rows.Close()
	h := sha256.New()
	var eligible int64
	for rows.Next() {
		var id int64
		var openalex, sourceHash, vectorHash string
		if err := rows.Scan(&id, &openalex, &sourceHash, &vectorHash); err != nil {
			return 0, 0, "", err
		}
		fmt.Fprintf(h, "%d:%d:%s:%s:%s\n", id, len(openalex), openalex, sourceHash, vectorHash)
		eligible++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, "", err
	}
	fmt.Fprintf(h, "total=%d;eligible=%d", total, eligible)
	return total, eligible, hex.EncodeToString(h.Sum(nil)), nil
}

type task struct {
	q      fixedQuery
	k      int
	method string
}

func ids(results []search.Result) []int64 {
	out := make([]int64, 0, len(results))
	for _, result := range results {
		out = append(out, result.ID)
	}
	return out
}

func execute(ctx context.Context, pool *pgxpool.Pool, cfg config, cfgSHA, fixtureVersion, fixtureSHA string, queries []fixedQuery) (*report, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	commit, dirty, err := gitState()
	if err != nil {
		return nil, err
	}
	r := &report{RunID: id, ExperimentID: cfg.ExperimentID, ConfigVersion: cfg.SchemaVersion,
		ConfigSHA256: cfgSHA, GitCommit: commit, GitDirty: dirty,
		DatasetSnapshot: cfg.DatasetSnapshot, ModelID: search.ModelID,
		ModelRevision: search.ModelRevision, TextVersion: search.TextVersion,
		QuerySetVersion: fixtureVersion, QuerySetSHA256: fixtureSHA, Seed: cfg.Seed,
		WarmupIterations: cfg.WarmupIterations, MeasuredIterations: cfg.MeasuredIterations,
		IndexName: search.HNSWIndexName, IndexBuildNote: "index created by migration 004 before run; build duration not measured",
		Percentile: percentileDefinition, CreatedAt: time.Now().UTC()}
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version'),
		(SELECT extversion FROM pg_extension WHERE extname = 'vector')`).Scan(&r.PostgresVersion, &r.PgvectorVersion); err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `SELECT reloptions FROM pg_class WHERE oid = 'public.papers_embedding_hnsw_cosine_idx'::regclass`).Scan(&r.IndexBuildOptions); err != nil {
		return nil, fmt.Errorf("migration 004 index required: %w", err)
	}
	r.Papers, r.EligibleVectors, r.DatasetSHA256, err = snapshot(ctx, pool)
	if err != nil {
		return nil, err
	}
	if r.EligibleVectors == 0 {
		return nil, errors.New("no eligible embeddings; import papers and run embeddings first")
	}
	// Compute ground truth before measuring. Exact uses a materialized eligible
	// relation, so its ordering cannot be provided by the ANN index.
	ground := make(map[string][]search.Result)
	for _, q := range queries {
		for _, k := range cfg.K {
			results, err := search.Exact(ctx, pool, q.Vector, k)
			if err != nil {
				return nil, err
			}
			if len(results) == 0 {
				return nil, fmt.Errorf("query %s has no exact results", q.ID)
			}
			ground[fmt.Sprintf("%s/%d", q.ID, k)] = results
		}
	}
	var tasks []task
	for _, q := range queries {
		for _, k := range cfg.K {
			for _, method := range []string{"exact", "hnsw"} {
				params := json.RawMessage(`{}`)
				if method == "hnsw" {
					params = json.RawMessage(fmt.Sprintf(`{"ef_search":%d}`, cfg.HNSWEFSearch))
				}
				r.Queries = append(r.Queries, queryResult{QueryID: q.ID, QuerySHA256: q.SHA256,
					K: k, Method: method, Parameters: params})
				tasks = append(tasks, task{q, k, method})
			}
		}
	}
	// A deterministic seed changes the ordering of work within each pass.
	// Both methods see the same corpus; warm-up samples are discarded.
	rng := randv2.New(randv2.NewPCG(uint64(cfg.Seed), 0x5eed))
	for iteration := -cfg.WarmupIterations; iteration < cfg.MeasuredIterations; iteration++ {
		order := slices.Clone(tasks)
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, t := range order {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			start := time.Now()
			var got []search.Result
			if t.method == "exact" {
				got, err = search.Exact(ctx, pool, t.q.Vector, t.k)
			} else {
				got, err = search.HNSW(ctx, pool, t.q.Vector, t.k, cfg.HNSWEFSearch)
			}
			elapsed := float64(time.Since(start).Nanoseconds()) / 1e6
			if err != nil {
				return nil, err
			}
			if iteration < 0 {
				continue
			}
			expected := ground[fmt.Sprintf("%s/%d", t.q.ID, t.k)]
			recall, ok := search.RecallAtK(expected, got, t.k)
			if !ok || (t.method == "exact" && !slices.Equal(ids(expected), ids(got))) {
				return nil, fmt.Errorf("exact ground truth changed for %s at k=%d", t.q.ID, t.k)
			}
			for j := range r.Queries {
				row := &r.Queries[j]
				if row.QueryID == t.q.ID && row.K == t.k && row.Method == t.method {
					row.Samples = append(row.Samples, sample{iteration, elapsed, recall, ids(got)})
					break
				}
			}
		}
	}
	for i := range r.Queries {
		row := &r.Queries[i]
		times := make([]float64, 0, len(row.Samples))
		for _, s := range row.Samples {
			times = append(times, s.LatencyMS)
			row.Recall += s.Recall
		}
		row.Recall /= float64(len(row.Samples))
		row.P50MS, _ = percentile(times, 50)
		row.P95MS, _ = percentile(times, 95)
		row.P99MS, _ = percentile(times, 99)
		q := queryByID(queries, row.QueryID)
		literal, _ := search.VectorLiteral(q.Vector)
		if row.Method == "exact" {
			row.Plan, err = explain(ctx, pool, search.ExactSQL,
				[]any{literal, search.ModelID, search.ModelRevision, search.TextVersion, row.K}, 0)
		} else {
			row.Plan, err = explain(ctx, pool, search.HNSWSQL, []any{literal, row.K}, cfg.HNSWEFSearch)
		}
		if err != nil {
			return nil, err
		}
		row.IndexUsed = containsIndex(row.Plan, search.HNSWIndexName)
		if row.Method == "exact" && row.IndexUsed {
			return nil, errors.New("exact plan unexpectedly uses HNSW index")
		}
		row.Plan, err = redactPlan(row.Plan)
		if err != nil {
			return nil, err
		}
		if row.Method == "hnsw" && cfg.RequireHNSWIndex && !row.IndexUsed {
			return nil, fmt.Errorf("HNSW plan for %s k=%d did not use index; inspect query shape, statistics and dataset size", row.QueryID, row.K)
		}
	}
	total, eligible, hash, err := snapshot(ctx, pool)
	if err != nil {
		return nil, err
	}
	if total != r.Papers || eligible != r.EligibleVectors || hash != r.DatasetSHA256 {
		return nil, errors.New("dataset changed during experiment; results discarded")
	}
	return r, nil
}

func queryByID(queries []fixedQuery, id string) fixedQuery {
	for _, q := range queries {
		if q.ID == id {
			return q
		}
	}
	panic("validated query missing")
}

func allIndexed(rows []queryResult) bool {
	for _, row := range rows {
		if row.Method == "hnsw" && !row.IndexUsed {
			return false
		}
	}
	return true
}

func explain(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, efSearch int) (json.RawMessage, error) {
	if efSearch == 0 {
		var raw string
		err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
		return json.RawMessage(raw), err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', $1, true)`, strconv.Itoa(efSearch)); err != nil {
		return nil, err
	}
	var raw string
	err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
	return json.RawMessage(raw), err
}

func containsIndex(raw json.RawMessage, index string) bool {
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	if json.Unmarshal(raw, &plans) != nil {
		return false
	}
	for _, plan := range plans {
		if hasIndex(plan.Plan, index) {
			return true
		}
	}
	return false
}

func hasIndex(node map[string]any, index string) bool {
	if node["Index Name"] == index {
		return true
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		if subnode, ok := child.(map[string]any); ok && hasIndex(subnode, index) {
			return true
		}
	}
	return false
}

var vectorInPlan = regexp.MustCompile(`\[[0-9eE+.,\- ]+\]`)

// EXPLAIN includes parameter values in Filter/Order By strings. Remove every
// 384-dimensional numeric literal before persistence, export or stdout.
func redactPlan(raw json.RawMessage) (json.RawMessage, error) {
	var plan any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			return vectorInPlan.ReplaceAllStringFunc(v, func(candidate string) string {
				if strings.Count(candidate, ",") >= search.Dimension-1 {
					return "[REDACTED_VECTOR]"
				}
				return candidate
			})
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
		case map[string]any:
			for k := range v {
				v[k] = walk(v[k])
			}
		}
		return value
	}
	return json.Marshal(walk(plan))
}
