package metrics

import (
	"testing"

	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func TestMetricsAreRegistered(t *testing.T) {
	ReconciliationRate.WithLabelValues("runnerset", "success").Inc()
	CacheHits.WithLabelValues("secret").Inc()
	CacheMisses.WithLabelValues("scaleset").Inc()
	metrics, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	names := make(map[string]bool)
	for _, mf := range metrics {
		if mf.Name != nil {
			names[*mf.Name] = true
		}
	}
	expected := []string{
		"scaleset_reconciliation_rate_total",
		"scaleset_cache_hits_total",
		"scaleset_cache_misses_total",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q to be registered", name)
		}
	}
}

func TestRecordAndReadReconciliationRate(t *testing.T) {
	ReconciliationRate.WithLabelValues("test_controller", "success").Inc()
	metrics, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found bool
	for _, mf := range metrics {
		if mf.Name != nil && *mf.Name == "scaleset_reconciliation_rate_total" {
			found = true
			if len(mf.Metric) == 0 {
				t.Error("expected at least one sample for scaleset_reconciliation_rate_total")
			}
			break
		}
	}
	if !found {
		t.Error("scaleset_reconciliation_rate_total not found in gathered metrics")
	}
}
