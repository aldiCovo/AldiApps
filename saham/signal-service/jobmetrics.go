package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The daily job's own metrics. metrics.go is shared with the shop's services, so they live here.
var (
	jobRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saham_job_runs_total",
		Help: "Daily job runs, by result: ok, skipped (no trading or already done) or error.",
	}, []string{"result"})
	jobLastOK = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "saham_job_last_ok_timestamp_seconds",
		Help: "When the daily job last ran without error, including runs that found nothing to do.",
	})
	jobDuration = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "saham_job_duration_seconds",
		Help: "How long the last successful run took.",
	})
)
