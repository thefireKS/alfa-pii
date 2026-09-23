// Command loadtest drives a reproducible load profile against the /process
// endpoint of pii-service. It pre-generates synthetic texts from different
// categories with a fixed seed, so the cost of generating inputs is never
// attributed to the service. It supports three operation distributions, a
// scheduled send mode, a large-text scenario and a compatibility check, and
// writes a report plus raw results to .artifacts/.
//
// Usage:
//
//	go run ./cmd/loadtest -base http://127.0.0.1:8080 -mode sequential -duration 5m
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"alfa-hackathon.local/pii/internal/loadgen"
)

func main() {
	var (
		base          = flag.String("base", "http://127.0.0.1:8080", "service base URL")
		mode          = flag.String("mode", "sequential", "scenario mode: sequential|mask_dominant|restore_dominant")
		duration      = flag.Duration("duration", 5*time.Minute, "run duration")
		target        = flag.Float64("target", 330, "target RPS")
		peak          = flag.Float64("peak", 1000, "peak RPS during ramp")
		ramp          = flag.Duration("ramp", 60*time.Second, "ramp-up duration")
		burstEvery    = flag.Duration("burst-every", 0, "interval between bursts (0 disables bursts)")
		burstDur      = flag.Duration("burst-duration", 0, "duration of each burst")
		workers       = flag.Int("workers", 200, "concurrent connections")
		seed          = flag.Int64("seed", loadgen.DefaultSeed, "generator seed")
		outDir        = flag.String("out", ".artifacts", "output directory")
		container     = flag.String("container", "pii-service", "container name for docker stats")
		prep          = flag.Int("prep", 0, "preparation count for restore_dominant")
		restoreFrac   = flag.Float64("restore-fraction", 0.5, "fraction of restore steps")
		restoreBudget = flag.Int("restore-budget", 1, "max restore attempts per pair")
		restoreDelay  = flag.Duration("restore-delay", 0, "pause between a completed mask and its restore step")
		totalPairs    = flag.Int("pairs", 100000, "total distinct payload_ids")
		small         = flag.Int("small", 120, "small payload size in chars")
		medium        = flag.Int("medium", 400, "medium payload size in chars")
		large         = flag.Int("large", 2000, "large payload size in chars")
		largeText     = flag.Bool("large-text", false, "run the large-text scenario")
		scheduled     = flag.Bool("scheduled", false, "run the scheduled send mode")
		compat        = flag.Bool("compat", false, "run the compatibility check")
		queue         = flag.Int("queue", 64, "pacer/scheduler queue size")
		grace         = flag.Duration("grace", 2*time.Second, "drain grace for in-flight requests after the run")
		prepTimeout   = flag.Duration("prep-timeout", 10*time.Minute, "timeout for the restore_dominant preparation phase")
	)
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}

	client := loadgen.NewClient(*base, *workers, 10*time.Second)
	poller := loadgen.NewMetricsPoller(*base, *container)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	runID := newRunID()
	command := strings.Join(os.Args, " ")

	switch {
	case *largeText:
		runLargeText(ctx, client, poller, *base, *outDir, *seed)
		return
	case *scheduled:
		runScheduled(ctx, client, poller, *base, *outDir, *seed, *target, *workers, *queue, *totalPairs, *restoreFrac, *restoreBudget, *restoreDelay, *small, *medium, *large, *grace, runID, command)
		return
	case *compat:
		runCompat(ctx, client, *base, *outDir, *seed, *small, *medium, *large)
		return
	}

	runMain(client, poller, loadgen.Mode(*mode), *base, *outDir, *seed,
		*target, *peak, *ramp, *burstEvery, *burstDur, *workers, *queue, *prep, *restoreFrac, *restoreBudget, *restoreDelay,
		*totalPairs, *small, *medium, *large, *duration, *grace, *prepTimeout, runID, command)
}

// runMain runs the main load profile. The preparation phase (restore_dominant)
// uses its own context and is excluded from the measured interval, so the
// reported duration covers only the measured part.
func runMain(client *loadgen.Client, poller *loadgen.MetricsPoller,
	mode loadgen.Mode, base, outDir string, seed int64, target, peak float64, ramp, burstEvery, burstDur time.Duration,
	workers, queue, prep int, restoreFrac float64, restoreBudget int, restoreDelay time.Duration,
	totalPairs, small, medium, large int, duration, grace, prepTimeout time.Duration, runID, command string) {

	gen := loadgen.NewTextGenerator(seed, loadgen.DefaultSizeProfile(), small, medium, large)
	cfg := loadgen.ScenarioConfig{
		Mode:            mode,
		TotalPairs:      totalPairs,
		RestoreFraction: restoreFrac,
		RestoreBudget:   restoreBudget,
		RestoreDelay:    restoreDelay,
		Seed:            seed,
		RunID:           runID,
	}

	scenario := loadgen.NewScenario(cfg, gen)
	var prepNote string
	if mode == loadgen.ModeRestoreDominant {
		// Preparation runs in its own context so its cost is never attributed
		// to the measured interval.
		prepCtx, prepCancel := context.WithTimeout(context.Background(), prepTimeout)
		prepNote = runPreparation(prepCtx, client, scenario, gen, prep, restoreBudget, cfg.RunID)
		prepCancel()
	}

	// The measured interval starts after preparation completes.
	runCtx, runCancel := context.WithTimeout(context.Background(), duration)
	defer runCancel()

	pacer := loadgen.NewPacer(loadgen.PacerConfig{
		Target:        target,
		Peak:          peak,
		BurstEvery:    burstEvery,
		BurstDuration: burstDur,
		Ramp:          ramp,
		Queue:         queue,
	})
	defer pacer.Stop()

	runner := loadgen.NewRunner(client, scenario, pacer, loadgen.RunnerConfig{
		Workers:               workers,
		Retry:                 loadgen.DefaultRetryPolicy(),
		MaxConsecutiveInvalid: 5,
		Grace:                 grace,
	})

	// Poll metrics periodically during the run. The poller context is derived
	// from the run context so it stops when the run stops early.
	pollCtx, pollCancel := context.WithCancel(runCtx)
	defer pollCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				poller.Poll(pollCtx)
			}
		}
	}()

	start := time.Now()
	stopReason := runner.Run(runCtx)
	measured := runner.Measured()
	if measured <= 0 {
		measured = time.Since(start)
	}
	pollCancel()
	poller.Poll(context.Background())

	granted, consumed, skipped := pacer.Counts()
	sent, maskSuccess, restoreSuccess, restoreMismatch, maskNoChange, canceled := runner.Counts()
	sm, peakCPU, peakRSS := poller.Snapshot()
	genRes := loadgen.SampleGeneratorResources()

	rep := &loadgen.Report{
		Title:              "Main load profile",
		Mode:               mode,
		Seed:               seed,
		RunID:              runID,
		Command:            command,
		Duration:           measured,
		Grace:              grace,
		TargetRPS:          target,
		PeakRPS:            peak,
		Ramp:               ramp,
		BurstEvery:         burstEvery,
		BurstDuration:      burstDur,
		Granted:            granted,
		Consumed:           consumed,
		Skipped:            skipped,
		Overdue:            pacer.Overdue(),
		Sent:               sent,
		MaskSuccess:        maskSuccess,
		RestoreSuccess:     restoreSuccess,
		RestoreMismatch:    restoreMismatch,
		MaskNoChange:       maskNoChange,
		Canceled:           canceled,
		StopReason:         stopReason,
		Stats:              runner.Stats(),
		LatencyByClass:     runner.Stats().LatencyByClass(),
		OpLatency:          runner.Stats().OpLatency(),
		ActualRPS:          rps(sent, measured),
		SuccessRPS:         rps(maskSuccess+restoreSuccess, measured),
		OverloadFraction:   fraction(runner.Stats().OutcomeCount(loadgen.OutcomeOverload), sent),
		Pending:            scenario.Pending(),
		StoreRecordsPending: sm.StoreRecordsPending,
		StoreRecordsReplay:  sm.StoreRecordsReplay,
		StoreBytesPending:   sm.StoreBytesPending,
		StoreBytesReplay:    sm.StoreBytesReplay,
		StoreFailures:       sm.StoreFailures,
		StoreTTL:            sm.StoreTTL,
		Active:              sm.Active,
		MetricsOK:           sm.OK(),
		CPUPercent:          peakCPU,
		RSSBytes:            peakRSS,
		GeneratorHeap:       genRes.HeapInUse,
		StoreDynamics:       poller.StoreDynamics(),
		ContainerLimits:     loadgen.ContainerLimits(containerName()),
		SizeProfile:         fmt.Sprintf("small=%d medium=%d large=%d", small, medium, large),
		OpFraction:          fmt.Sprintf("restore_fraction=%.2f restore_budget=%d restore_delay=%s", restoreFrac, restoreBudget, restoreDelay),
		Preparation:         prepNote,
		Notes: []string{
			"Token estimate is runes/4 (no exact tokenizer); sizes are in bytes and chars.",
			"Generator memory is separate from service memory; inputs are pre-generated.",
			"Latency is measured until the full response body is read; TTFB is separate.",
			"Run end and per-request timeout are classified separately; unfinished operations are counted as canceled.",
		},
	}

	writeReport(outDir, "main-"+string(mode)+".txt", rep)
	writeJSON(outDir, "main-"+string(mode)+".json", rep)
	fmt.Print(rep.String())
}

// runPreparation creates the pre-created set for restore_dominant. It returns a
// description of the preparation cost and size. Each successfully created
// correspondence is registered in the scenario together with its payload_id so
// it can be restored later under the exact ID.
func runPreparation(ctx context.Context, client *loadgen.Client, scenario loadgen.Scenario, gen *loadgen.TextGenerator, count, budget int, runID string) string {
	if count <= 0 {
		return "no preparation (restore_dominant with no pre-created set)"
	}
	prepared, ok := scenario.(loadgen.PreparedScenario)
	if !ok {
		return "preparation skipped: scenario does not support pre-created set"
	}
	start := time.Now()
	success := 0
	var bytes int64
	for i := 0; i < count; i++ {
		text, _ := gen.Next()
		id := prepID(runID, i)
		resp, _ := client.SendWithRetry(ctx, id, text, loadgen.DefaultRetryPolicy())
		if resp.IsValidSuccess() && resp.Result != text {
			prepared.AddPrepared(id, text, resp.Result, budget)
			success++
			bytes += int64(len(text))
		}
	}
	dur := time.Since(start)
	return fmt.Sprintf("created %d/%d correspondences in %s, %d bytes, budget=%d", success, count, dur.Round(time.Millisecond), bytes, budget)
}

// runLargeText runs the large-text scenario up to the 100k-token limit. It
// reports bytes, runes, the token estimation method, per-stage latency and peak
// RSS. The report depends on the current service response: a text inside the
// supported profile must mask and restore with HTTP 200, and exceeding the
// declared limit must give a predictable failure.
func runLargeText(ctx context.Context, client *loadgen.Client, poller *loadgen.MetricsPoller, base, outDir string, seed int64) {
	// Token estimate is runes/4, so 100k tokens ~= 400k runes. Test a range of
	// sizes in tokens: 10k, 50k, 100k.
	tokenTargets := []int{10000, 50000, 100000}
	gen := loadgen.NewTextGenerator(seed, loadgen.DefaultSizeProfile(), 120, 400, 2000)
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	w("=== Large-text scenario ===")
	w("Token estimate: runes/4 (no exact tokenizer). Sizes reported in bytes and chars.")
	w("Seed: %d", seed)
	for _, tokens := range tokenTargets {
		runes := tokens * 4
		text := buildLargeText(gen, runes)
		id := fmt.Sprintf("large-%d", tokens)
		start := time.Now()
		resp, _ := client.SendWithRetry(ctx, id, text, loadgen.DefaultRetryPolicy())
		latency := time.Since(start)
		w("tokens=%d runes=%d bytes=%d chars=%d status=%d latency=%s result_len=%d",
			tokens, runes, len(text), runeCount(text), resp.Status, latency.Round(time.Microsecond), len(resp.Result))
		if resp.IsValidSuccess() {
			// Restore the mask to verify exact restoration.
			start = time.Now()
			restoreResp, _ := client.SendWithRetry(ctx, id, resp.Result, loadgen.DefaultRetryPolicy())
			restoreLatency := time.Since(start)
			ok := restoreResp.IsValidSuccess() && restoreResp.Result == text
			w("  restore status=%d latency=%s exact=%v", restoreResp.Status, restoreLatency.Round(time.Microsecond), ok)
		}
	}
	// Numerous-entities case: a large text (~100k tokens) made of many emails.
	// This exercises the per-record limit and the replacement table with a dense
	// entity list, not just a single entity in a filler string.
	runes := 400_000
	text := buildManyEntitiesText(runes)
	id := "large-many-entities"
	start := time.Now()
	resp, _ := client.SendWithRetry(ctx, id, text, loadgen.DefaultRetryPolicy())
	latency := time.Since(start)
	w("many_entities runes=%d bytes=%d chars=%d status=%d latency=%s result_len=%d",
		runes, len(text), runeCount(text), resp.Status, latency.Round(time.Microsecond), len(resp.Result))
	if resp.IsValidSuccess() {
		start = time.Now()
		restoreResp, _ := client.SendWithRetry(ctx, id, resp.Result, loadgen.DefaultRetryPolicy())
		restoreLatency := time.Since(start)
		ok := restoreResp.IsValidSuccess() && restoreResp.Result == text
		w("  restore status=%d latency=%s exact=%v", restoreResp.Status, restoreLatency.Round(time.Microsecond), ok)
	}
	// Report peak RSS from the poller (docker stats for a container, or the
	// service process for a local run).
	poller.Poll(context.Background())
	_, _, peakRSS := poller.Snapshot()
	if peakRSS <= 0 {
		peakRSS = measureServiceRSS(base)
	}
	w("peak_rss_bytes=%d", peakRSS)
	writeFile(outDir, "large-text.txt", b.String())
	fmt.Print(b.String())
}

// buildLargeText builds a text of approximately the given rune count by
// repeating a synthetic fragment with filler. It is deterministic for a given
// generator seed.
func buildLargeText(gen *loadgen.TextGenerator, runes int) string {
	base, _ := gen.Next()
	for runeCount(base) < runes {
		base += " " + gen.FillerWord()
	}
	return base
}

// buildManyEntitiesText builds a text of approximately the given rune count
// made of many emails, so the mask and restore exercise a dense entity list.
func buildManyEntitiesText(runes int) string {
	var b strings.Builder
	for i := 0; runeCount(b.String()) < runes; i++ {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "email user%d@example.test", i)
	}
	return b.String()
}

// measureServiceRSS returns the resident set size in bytes of the process
// listening on the port of the given base URL, or 0 when it cannot be
// determined. It is a fallback for local runs without a container.
func measureServiceRSS(base string) int64 {
	u, err := url.Parse(base)
	if err != nil {
		return 0
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	out, err := exec.Command("lsof", "-ti", "tcp:"+port, "-s", "tcp:listen").Output()
	if err != nil {
		return 0
	}
	pid := strings.TrimSpace(string(out))
	if pid == "" {
		return 0
	}
	ps, err := exec.Command("ps", "-o", "rss=", "-p", pid).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(ps)), 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}

// runScheduled runs the scheduled send mode.
func runScheduled(ctx context.Context, client *loadgen.Client, poller *loadgen.MetricsPoller,
	base, outDir string, seed int64, target float64, workers, queue, totalPairs int, restoreFrac float64, restoreBudget int, restoreDelay time.Duration, small, medium, large int, grace time.Duration, runID, command string) {

	gen := loadgen.NewTextGenerator(seed, loadgen.DefaultSizeProfile(), small, medium, large)
	scenario := loadgen.NewScenario(loadgen.ScenarioConfig{
		Mode:            loadgen.ModeMaskDominant,
		TotalPairs:      totalPairs,
		RestoreFraction: restoreFrac,
		RestoreBudget:   restoreBudget,
		RestoreDelay:    restoreDelay,
		Seed:            seed,
		RunID:           runID,
	}, gen)
	sched := loadgen.NewScheduler(client, scenario, loadgen.SchedulerConfig{
		Target:                target,
		Workers:               workers,
		Queue:                 queue,
		Retry:                 loadgen.DefaultRetryPolicy(),
		MaxConsecutiveInvalid: 5,
		Grace:                 grace,
	})

	// The poller context is derived from the run context so it stops when the
	// scheduler stops early.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	pollCtx, pollCancel := context.WithCancel(runCtx)
	defer pollCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				poller.Poll(pollCtx)
			}
		}
	}()

	start := time.Now()
	stopReason := sched.Run(runCtx)
	measured := sched.Measured()
	if measured <= 0 {
		measured = time.Since(start)
	}
	pollCancel()
	poller.Poll(context.Background())

	scheduled, sent, late, skipped, maskSuccess, restoreSuccess, restoreMismatch, maskNoChange, overdue, canceled := sched.Counts()
	sm, peakCPU, peakRSS := poller.Snapshot()
	genRes := loadgen.SampleGeneratorResources()

	rep := &loadgen.Report{
		Title:               "Scheduled send mode",
		Mode:                loadgen.ModeMaskDominant,
		Seed:                seed,
		RunID:               runID,
		Command:             command,
		Duration:            measured,
		Grace:               grace,
		TargetRPS:           target,
		Granted:             scheduled,
		Consumed:            sent,
		Skipped:             skipped,
		Overdue:             overdue,
		Late:                late,
		Sent:                sent,
		MaskSuccess:         maskSuccess,
		RestoreSuccess:      restoreSuccess,
		RestoreMismatch:     restoreMismatch,
		MaskNoChange:        maskNoChange,
		Canceled:            canceled,
		StopReason:          stopReason,
		Stats:               sched.Stats(),
		LatencyByClass:      sched.Stats().LatencyByClass(),
		OpLatency:           sched.Stats().OpLatency(),
		ActualRPS:           rps(sent, measured),
		SuccessRPS:          rps(maskSuccess+restoreSuccess, measured),
		OverloadFraction:    fraction(sched.Stats().OutcomeCount(loadgen.OutcomeOverload), sent),
		Pending:             scenario.Pending(),
		StoreRecordsPending: sm.StoreRecordsPending,
		StoreRecordsReplay:  sm.StoreRecordsReplay,
		StoreBytesPending:   sm.StoreBytesPending,
		StoreBytesReplay:    sm.StoreBytesReplay,
		StoreFailures:       sm.StoreFailures,
		StoreTTL:            sm.StoreTTL,
		Active:              sm.Active,
		MetricsOK:           sm.OK(),
		CPUPercent:          peakCPU,
		RSSBytes:            peakRSS,
		GeneratorHeap:       genRes.HeapInUse,
		StoreDynamics:       poller.StoreDynamics(),
		ContainerLimits:     loadgen.ContainerLimits(containerName()),
		SizeProfile:         fmt.Sprintf("small=%d medium=%d large=%d", small, medium, large),
		OpFraction:          fmt.Sprintf("restore_fraction=%.2f restore_budget=%d restore_delay=%s", restoreFrac, restoreBudget, restoreDelay),
		Notes: []string{
			"Scheduled mode: fixed rate, bounded queue, late sends and skips visible.",
			"Initial attempts and retries share one common HTTP budget.",
			"Run end and per-request timeout are classified separately; unfinished operations are counted as canceled.",
		},
	}
	writeReport(outDir, "scheduled.txt", rep)
	writeJSON(outDir, "scheduled.json", rep)
	fmt.Print(rep.String())
}

// runCompat runs the compatibility check: stop after five consecutive invalid
// responses; 429 does not increment or reset the counter; success resets it.
func runCompat(ctx context.Context, client *loadgen.Client, base, outDir string, seed int64, small, medium, large int) {
	gen := loadgen.NewTextGenerator(seed, loadgen.DefaultSizeProfile(), small, medium, large)
	scenario := loadgen.NewScenario(loadgen.ScenarioConfig{
		Mode:       loadgen.ModeSequential,
		TotalPairs: 1000,
		Seed:       seed,
	}, gen)
	pacer := loadgen.NewPacer(loadgen.PacerConfig{Target: 50, Ramp: 0, Queue: 8})
	defer pacer.Stop()
	runner := loadgen.NewRunner(client, scenario, pacer, loadgen.RunnerConfig{
		Workers:               20,
		Retry:                 loadgen.DefaultRetryPolicy(),
		MaxConsecutiveInvalid: 5,
	})
	start := time.Now()
	stopReason := runner.Run(ctx)
	duration := time.Since(start)
	sent, maskSuccess, restoreSuccess, _, _, _ := runner.Counts()
	rep := &loadgen.Report{
		Title:          "Compatibility check",
		Mode:           loadgen.ModeSequential,
		Seed:           seed,
		Duration:       duration,
		TargetRPS:      50,
		Sent:           sent,
		MaskSuccess:    maskSuccess,
		RestoreSuccess: restoreSuccess,
		StopReason:     stopReason,
		Stats:          runner.Stats(),
		Notes: []string{
			"Stops after five consecutive invalid responses; 429 neither increments nor resets; success resets.",
		},
	}
	writeReport(outDir, "compat.txt", rep)
	writeJSON(outDir, "compat.json", rep)
	fmt.Print(rep.String())
}

func containerName() string {
	if v := os.Getenv("PII_LOADTEST_CONTAINER"); v != "" {
		return v
	}
	return "pii-service"
}

func writeReport(outDir, name string, rep *loadgen.Report) {
	writeFile(outDir, name, rep.String())
}

func writeJSON(outDir, name string, rep *loadgen.Report) {
	type jsonStats struct {
		Count    int64                     `json:"count"`
		Outcomes map[loadgen.Outcome]int64 `json:"outcomes"`
		Statuses map[int]int64             `json:"statuses"`
		Bytes    int64                     `json:"bytes"`
		Chars    int64                     `json:"chars"`
	}
	type jsonLatency struct {
		Mean string `json:"mean"`
		P50  string `json:"p50"`
		P95  string `json:"p95"`
		P99  string `json:"p99"`
	}
	type jsonSample struct {
		At       string `json:"at"`
		Records  int64  `json:"records"`
		Bytes    int64  `json:"bytes"`
		Mask     int64  `json:"mask"`
		Restore  int64  `json:"restore"`
		Overload int64  `json:"overload"`
		Errors   int64  `json:"errors"`
		OK       bool   `json:"ok"`
	}
	type jsonReport struct {
		Title               string                                `json:"title"`
		Mode                loadgen.Mode                          `json:"mode"`
		Seed                int64                                 `json:"seed"`
		RunID               string                                `json:"run_id"`
		Command             string                                `json:"command"`
		Duration            string                                `json:"duration"`
		Grace               string                                `json:"grace"`
		TargetRPS           float64                               `json:"target_rps"`
		PeakRPS             float64                               `json:"peak_rps"`
		Ramp                string                                `json:"ramp"`
		BurstEvery          string                                `json:"burst_every"`
		BurstDuration       string                                `json:"burst_duration"`
		Granted             int64                                 `json:"granted"`
		Consumed            int64                                 `json:"consumed"`
		Skipped             int64                                 `json:"skipped"`
		Overdue             int64                                 `json:"overdue"`
		Late                int64                                 `json:"late"`
		Sent                int64                                 `json:"sent"`
		MaskSuccess         int64                                 `json:"mask_success"`
		RestoreSuccess      int64                                 `json:"restore_success"`
		RestoreMismatch     int64                                 `json:"restore_mismatch"`
		MaskNoChange        int64                                 `json:"mask_no_change"`
		Canceled            int64                                 `json:"canceled"`
		StopReason          string                                `json:"stop_reason"`
		Pending             int                                   `json:"pending"`
		StoreRecordsPending int64                                 `json:"store_records_pending"`
		StoreRecordsReplay  int64                                 `json:"store_records_replay"`
		StoreBytesPending   int64                                 `json:"store_bytes_pending"`
		StoreBytesReplay    int64                                 `json:"store_bytes_replay"`
		StoreFailures       map[string]int64                      `json:"store_failures"`
		StoreTTL            int64                                 `json:"store_ttl"`
		Active              int64                                 `json:"active"`
		MetricsOK           bool                                  `json:"metrics_ok"`
		CPUPercent          float64                               `json:"cpu_percent"`
		RSSBytes            int64                                 `json:"rss_bytes"`
		GeneratorHeap       int64                                 `json:"generator_heap"`
		ContainerLimits     string                                `json:"container_limits"`
		SizeProfile         string                                `json:"size_profile"`
		OpFraction          string                                `json:"op_fraction"`
		Preparation         string                                `json:"preparation"`
		Notes               []string                              `json:"notes"`
		Stats               *jsonStats                            `json:"stats"`
		Latency             *jsonLatency                          `json:"latency"`
		LatencyByClass      map[loadgen.LatencyClass]*jsonLatency `json:"latency_by_class"`
		OpLatency           *jsonLatency                          `json:"op_latency"`
		ActualRPS           float64                               `json:"actual_rps"`
		SuccessRPS          float64                               `json:"success_rps"`
		OverloadFraction    float64                               `json:"overload_fraction"`
		StoreDynamics       []jsonSample                          `json:"store_dynamics"`
	}
	jr := &jsonReport{
		Title:               rep.Title,
		Mode:                rep.Mode,
		Seed:                rep.Seed,
		RunID:               rep.RunID,
		Command:             rep.Command,
		Duration:            rep.Duration.String(),
		Grace:               rep.Grace.String(),
		TargetRPS:           rep.TargetRPS,
		PeakRPS:             rep.PeakRPS,
		Ramp:                rep.Ramp.String(),
		BurstEvery:          rep.BurstEvery.String(),
		BurstDuration:       rep.BurstDuration.String(),
		Granted:             rep.Granted,
		Consumed:            rep.Consumed,
		Skipped:             rep.Skipped,
		Overdue:             rep.Overdue,
		Late:                rep.Late,
		Sent:                rep.Sent,
		MaskSuccess:         rep.MaskSuccess,
		RestoreSuccess:      rep.RestoreSuccess,
		RestoreMismatch:     rep.RestoreMismatch,
		MaskNoChange:        rep.MaskNoChange,
		Canceled:            rep.Canceled,
		StopReason:          rep.StopReason,
		Pending:             rep.Pending,
		StoreRecordsPending: rep.StoreRecordsPending,
		StoreRecordsReplay:  rep.StoreRecordsReplay,
		StoreBytesPending:   rep.StoreBytesPending,
		StoreBytesReplay:    rep.StoreBytesReplay,
		StoreFailures:       rep.StoreFailures,
		StoreTTL:            rep.StoreTTL,
		Active:              rep.Active,
		MetricsOK:           rep.MetricsOK,
		CPUPercent:          rep.CPUPercent,
		RSSBytes:            rep.RSSBytes,
		GeneratorHeap:       rep.GeneratorHeap,
		ContainerLimits:     rep.ContainerLimits,
		SizeProfile:         rep.SizeProfile,
		OpFraction:          rep.OpFraction,
		Preparation:         rep.Preparation,
		Notes:               rep.Notes,
		ActualRPS:           rep.ActualRPS,
		SuccessRPS:          rep.SuccessRPS,
		OverloadFraction:    rep.OverloadFraction,
	}
	if rep.Stats != nil {
		snap := rep.Stats.Snapshot()
		jr.Stats = &jsonStats{
			Count:    snap.Count,
			Outcomes: snap.Outcomes,
			Statuses: snap.Statuses,
			Bytes:    snap.Bytes,
			Chars:    snap.Chars,
		}
		mean, pcts := rep.Stats.Percentiles(50, 95, 99)
		jr.Latency = &jsonLatency{
			Mean: mean.String(),
			P50:  pcts[50].String(),
			P95:  pcts[95].String(),
			P99:  pcts[99].String(),
		}
	}
	if len(rep.LatencyByClass) > 0 {
		jr.LatencyByClass = make(map[loadgen.LatencyClass]*jsonLatency, len(rep.LatencyByClass))
		for class, s := range rep.LatencyByClass {
			jr.LatencyByClass[class] = &jsonLatency{
				Mean: s.Mean.String(),
				P50:  s.P50.String(),
				P95:  s.P95.String(),
				P99:  s.P99.String(),
			}
		}
	}
	if rep.OpLatency.Mean > 0 {
		jr.OpLatency = &jsonLatency{
			Mean: rep.OpLatency.Mean.String(),
			P50:  rep.OpLatency.P50.String(),
			P95:  rep.OpLatency.P95.String(),
			P99:  rep.OpLatency.P99.String(),
		}
	}
	if len(rep.StoreDynamics) > 0 {
		jr.StoreDynamics = make([]jsonSample, 0, len(rep.StoreDynamics))
		for _, s := range rep.StoreDynamics {
			jr.StoreDynamics = append(jr.StoreDynamics, jsonSample{
				At:       s.At.Format(time.RFC3339),
				Records:  s.Records,
				Bytes:    s.Bytes,
				Mask:     s.Mask,
				Restore:  s.Restore,
				Overload: s.Overload,
				Errors:   s.Errors,
				OK:       s.OK,
			})
		}
	}
	data, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return
	}
	writeFile(outDir, name, string(data))
}

func writeFile(outDir, name, content string) {
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// prepID builds the payload_id used by the preparation phase for a prepared
// pair. It must match the ID used when the correspondence was created.
func prepID(runID string, i int) string {
	return "prep-" + runID + "-" + itoa(i)
}

// newRunID returns a unique identifier for a load run. It is embedded in every
// payload_id so a repeated run against a live service does not collide with
// correspondences created by an earlier run. The seed is unaffected, so the
// generated texts remain reproducible.
func newRunID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// rps returns the rate of count events over the given duration.
func rps(count int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(count) / d.Seconds()
}

// fraction returns count/total, or 0 when total is zero.
func fraction(count, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
