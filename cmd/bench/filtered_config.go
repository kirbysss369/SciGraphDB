package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/kirbysss369/SciGraphDB/internal/search"
)

type filteredConfig struct {
	SchemaVersion      int    `json:"schema_version"`
	ExperimentID       string `json:"experiment_id"`
	DatasetSnapshot    string `json:"dataset_snapshot"`
	QuerySet           string `json:"query_set"`
	Seed               int64  `json:"seed"`
	K                  []int  `json:"k"`
	TargetPercentages  []int  `json:"target_selectivity_pct"`
	WarmupIterations   int    `json:"warmup_iterations"`
	MeasuredIterations int    `json:"measured_iterations"`
	IVFLists           int    `json:"ivfflat_lists"`
	IVFProbes          int    `json:"ivfflat_probes"`
	IVFMaxProbes       int    `json:"ivfflat_max_probes"`
	HNSWEFSearch       int    `json:"hnsw_ef_search"`
	HNSWIterativeScan  string `json:"hnsw_iterative_scan"`
	IVFIterativeScan   string `json:"ivfflat_iterative_scan"`
}

func readFilteredConfig(path string) (filteredConfig, string, error) {
	var cfg filteredConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, "", err
	}
	if err := decodeStrict(data, &cfg); err != nil {
		return cfg, "", err
	}
	if err := cfg.validate(); err != nil {
		return cfg, "", err
	}
	return cfg, digest(data), nil
}

func (c filteredConfig) validate() error {
	base := config{SchemaVersion: 1, ExperimentID: c.ExperimentID, DatasetSnapshot: c.DatasetSnapshot,
		QuerySet: c.QuerySet, Seed: c.Seed, K: c.K, WarmupIterations: c.WarmupIterations,
		MeasuredIterations: c.MeasuredIterations, HNSWEFSearch: c.HNSWEFSearch}
	if c.SchemaVersion != 2 {
		return errors.New("filtered config must use schema_version 2")
	}
	if err := base.validate(); err != nil {
		return err
	}
	if !slices.Equal(c.K, []int{10, 20}) || !slices.Equal(c.TargetPercentages, []int{100, 50, 25, 10, 5, 1}) {
		return errors.New("fixed filtered experiment requires k=[10,20] and targets=[100,50,25,10,5,1]")
	}
	if c.IVFLists < 2 || c.IVFLists > 1000 || c.IVFProbes < 1 || c.IVFProbes >= c.IVFLists ||
		c.IVFMaxProbes < c.IVFProbes || c.IVFMaxProbes > c.IVFLists {
		return fmt.Errorf("invalid IVFFlat lists/probes/max_probes; probes must be below lists")
	}
	return (search.FilterOptions{EFSearch: c.HNSWEFSearch, Probes: c.IVFProbes,
		MaxProbes: c.IVFMaxProbes, HNSWIterative: c.HNSWIterativeScan, IVFIterative: c.IVFIterativeScan}).Validate()
}

func configVersion(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var header struct {
		Version int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, err
	}
	return header.Version, nil
}
