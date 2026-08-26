package metrics

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	"github.com/prometheus/client_golang/prometheus"
)

// WriteAnalysisTextfile materializes run-scoped, ledgerable metrics strictly
// from retained evidence. These are cumulative snapshots, not live counters;
// dashboards must query the raw buckets and must not apply rate().
func WriteAnalysisTextfile(path string, groups []evidence.GroupResult, placements []evidence.PlacementEvent, oracles []replay.OracleRecord) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("analysis metrics path is required")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("analysis metrics output %q already exists", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect analysis metrics output: %w", err)
	}

	registry := prometheus.NewRegistry()
	placementTargets := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "velaserve_placement_target_total",
		Help: "Run-scoped count of retained EPP placements by bounded deployment identity.",
	}, []string{"arm", "width", "target"})
	oracleRegret := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "velaserve_oracle_regret_seconds",
		Help:    "Offline Arm-B predicted makespan minus the N-aware oracle best makespan, in seconds.",
		Buckets: durationBuckets(),
	}, []string{"width", "load", "prefix", "transport"})
	dispatchToEPP := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "velaserve_dispatch_to_epp_observation_seconds",
		Help:    "Observed delay from client dispatch timestamp to the correlated EPP scheduling record; not internal scheduler CPU time.",
		Buckets: durationBuckets(),
	}, []string{"arm", "width"})
	ordinaryTrafficLatency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "velaserve_ordinary_traffic_latency_seconds",
		Help:    "Condition-driver mean latency for ordinary offered traffic over the finalized full group window.",
		Buckets: durationBuckets(),
	}, []string{"load", "transport"})
	for _, collector := range []prometheus.Collector{placementTargets, oracleRegret, dispatchToEPP, ordinaryTrafficLatency} {
		if err := registry.Register(collector); err != nil {
			return err
		}
	}

	groupsByKey := make(map[string]evidence.GroupResult, len(groups))
	childrenByKey := make(map[string]evidence.ChildResult)
	for index, group := range groups {
		groupKey := analysisGroupKey(group.RunID, group.GroupID)
		if _, exists := groupsByKey[groupKey]; exists {
			return fmt.Errorf("analysis metrics group %d duplicates run/group identity", index+1)
		}
		groupsByKey[groupKey] = group
		for _, child := range group.Children {
			childKey := analysisChildKey(group.RunID, group.GroupID, child.RequestID)
			if _, exists := childrenByKey[childKey]; exists {
				return fmt.Errorf("analysis metrics duplicate child identity %q", child.RequestID)
			}
			childrenByKey[childKey] = child
		}
		if group.Condition != nil && group.Condition.FinalizedAt != nil {
			ordinaryTrafficLatency.WithLabelValues(string(group.Cell.LoadRegime), group.Cell.Transport).Observe(group.Condition.ObservedState.OrdinaryTrafficMeanLatencySeconds)
		}
	}

	for index, placement := range placements {
		group, exists := groupsByKey[analysisGroupKey(placement.RunID, placement.GroupID)]
		if !exists {
			return fmt.Errorf("analysis metrics placement %d has no retained group", index+1)
		}
		child, exists := childrenByKey[analysisChildKey(placement.RunID, placement.GroupID, placement.RequestID)]
		if !exists || child.DispatchedAt == nil {
			return fmt.Errorf("analysis metrics placement %d has no dispatched child", index+1)
		}
		delay := placement.ObservedAt.Sub(*child.DispatchedAt).Seconds()
		if math.IsNaN(delay) || math.IsInf(delay, 0) || delay < 0 {
			return fmt.Errorf("analysis metrics placement %d predates client dispatch", index+1)
		}
		width := strconv.FormatUint(uint64(group.FanoutWidth), 10)
		placementTargets.WithLabelValues(string(placement.Arm), width, placement.Target.ID).Inc()
		dispatchToEPP.WithLabelValues(string(placement.Arm), width).Observe(delay)
	}

	for index, oracle := range oracles {
		group, exists := groupsByKey[analysisGroupKey(oracle.RunID, oracle.GroupID)]
		if !exists {
			return fmt.Errorf("analysis metrics oracle %d has no retained group", index+1)
		}
		regret := oracle.ArmB.PredictedMakespan - oracle.Oracle.BestMakespan
		if math.IsNaN(regret) || math.IsInf(regret, 0) || regret < -1e-9 {
			return fmt.Errorf("analysis metrics oracle %d has invalid regret", index+1)
		}
		if regret < 0 {
			regret = 0
		}
		oracleRegret.WithLabelValues(strconv.FormatUint(uint64(group.FanoutWidth), 10), string(group.Cell.LoadRegime), group.Cell.PrefixRegime, group.Cell.Transport).Observe(regret)
	}

	if err := prometheus.WriteToTextfile(path, registry); err != nil {
		return fmt.Errorf("write analysis metrics: %w", err)
	}
	return nil
}

func analysisGroupKey(runID, groupID string) string { return runID + "\x00" + groupID }
func analysisChildKey(runID, groupID, requestID string) string {
	return runID + "\x00" + groupID + "\x00" + requestID
}
