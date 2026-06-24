package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/analyzer"
	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/parser"
	"github.com/Mapleeeeeeeeeee/cc-session-reader/internal/session"
)

func cmdBenchmark(args []string, reader session.TranscriptReader) {
	store := parser.DefaultStore()
	if hs, ok := reader.(session.HeaderScanner); ok {
		store = parser.DefaultStoreWith(hs)
	}
	exitOnError(runBenchmark(args, os.Stdout, os.Stderr, store, reader))
}

type pricing struct {
	CachedRead float64 // $/M tokens
	CacheWrite float64 // $/M tokens
	BaseInput  float64 // $/M tokens (uncached, after last breakpoint)
}

var pricingOpus = pricing{CachedRead: 0.50, CacheWrite: 6.25, BaseInput: 5.00}
var pricingSonnet = pricing{CachedRead: 0.30, CacheWrite: 3.75, BaseInput: 3.00}

const (
	tokenCountModelOpus46 = "claude-opus-4-6"
	tokenCountModelOpus47 = "claude-opus-4-7"
	tokenCountModelOpus48 = "claude-opus-4-8"
	tokenCountModelSonnet = "claude-sonnet-4-6"
)

type benchmarkModelConfig struct {
	pricing         pricing
	tokenCountModel string
}

func resolveBenchmarkModel(model string) (benchmarkModelConfig, error) {
	switch model {
	case "sonnet":
		return benchmarkModelConfig{pricing: pricingSonnet, tokenCountModel: tokenCountModelSonnet}, nil
	case "opus", "opus-4-8":
		return benchmarkModelConfig{pricing: pricingOpus, tokenCountModel: tokenCountModelOpus48}, nil
	case "opus-4-7":
		return benchmarkModelConfig{pricing: pricingOpus, tokenCountModel: tokenCountModelOpus47}, nil
	case "opus-4-6":
		return benchmarkModelConfig{pricing: pricingOpus, tokenCountModel: tokenCountModelOpus46}, nil
	default:
		return benchmarkModelConfig{}, fmt.Errorf("unknown model %q: must be opus, opus-4-6, opus-4-7, opus-4-8, or sonnet", model)
	}
}

type sessionBenchResult struct {
	shortID          string
	contextTokens    int
	filteredTokens   int
	newContextTokens int
	savedPct         float64
	callsPerTurn     float64
	toolIOPerCall    int // derived from actual PerTool data
	avgResponse      int // derived from TotalOutputTokens / APICallCount
	prompt           int // derived from context growth, or fallback perTurnPrompt
	breakEven        int
	saving10Pct      float64
	saving100Pct     float64
	warmBreakEven    int
	warmSaving10Pct  float64
	warmSaving100Pct float64
}

func runBenchmark(args []string, out io.Writer, errOut io.Writer, store parser.Store, reader session.TranscriptReader) error {
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	fs.SetOutput(errOut)
	days := fs.Int("days", 30, "how far back to scan")
	minKB := fs.Int("min-kb", 100, "minimum JSONL file size in KB")
	maxN := fs.Int("n", 10, "max sessions to include")
	model := fs.String("model", "opus", "model: opus, opus-4-6, opus-4-7, opus-4-8, or sonnet")
	overhead := fs.Int("overhead", 0, "session overhead tokens (system+tools+CLAUDE.md); measure with a 1-turn session")
	isNoAPI := fs.Bool("no-api", false, "skip API calls; estimate filtered-text tokens with chars/2 (offline fallback)")
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return err
	}

	logUsageAsync("benchmark", "")

	overheadToks := *overhead
	if overheadToks <= 0 {
		overheadToks = defaultOverhead
	}

	modelConfig, err := resolveBenchmarkModel(*model)
	if err != nil {
		return err
	}
	p := modelConfig.pricing
	tokenCountModel := modelConfig.tokenCountModel

	all, _ := store.ListAllSessions()

	cutoff := time.Now().AddDate(0, 0, -*days)

	type candidate struct {
		entry    parser.SessionListEntry
		fileSize int64
		path     string
	}

	var candidates []candidate
	for _, entry := range all {
		if !entry.StartTimeParsed.IsZero() && entry.StartTimeParsed.Before(cutoff) {
			continue
		}
		resolved, err := store.ResolveSession(entry.SessionID)
		if err != nil || resolved.Path == "" {
			continue
		}
		info, err := os.Stat(resolved.Path)
		if err != nil {
			continue
		}
		sizeKB := info.Size() / 1024
		if sizeKB < int64(*minKB) {
			continue
		}
		candidates = append(candidates, candidate{entry: entry, fileSize: info.Size(), path: resolved.Path})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].fileSize > candidates[j].fileSize
	})

	if len(candidates) == 0 {
		fmt.Fprintln(out, "No sessions matched the filters.")
		return nil
	}

	var results []sessionBenchResult
	var tokenCounter countTokensFunc
	for _, c := range candidates {
		if len(results) >= *maxN {
			break
		}
		events, err := reader.ReadAll(c.path)
		if err != nil {
			continue
		}
		stats := analyzer.ComputeStats(events)

		contextToks := stats.LastContextTokens
		if contextToks == 0 {
			fmt.Fprintf(out, "  skipping %s: missing API usage data\n",
				session.ShortID(c.entry.SessionID, 8))
			continue
		}

		if stats.CompactCount > 0 {
			fmt.Fprintf(out, "  skipping %s: compacted (%d times)\n",
				session.ShortID(c.entry.SessionID, 8), stats.CompactCount)
			continue
		}

		var filteredToks int
		if *isNoAPI {
			filteredToks = stats.FilteredChars / charsPerToken
		} else {
			if tokenCounter == nil {
				tokenCounter, err = newCountTokensFn(tokenCountModel)
				if err != nil {
					return fmt.Errorf("initialize token counter: %w", err)
				}
			}
			filteredToks, err = tokenCounter(stats.FilteredText)
			if err != nil {
				return fmt.Errorf("count filtered tokens for %s: %w", session.ShortID(c.entry.SessionID, 8), err)
			}
		}
		newContextToks := overheadToks + filteredToks

		savedPct := float64(contextToks-newContextToks) * 100.0 / float64(contextToks)

		cpt := 1.0
		if stats.UserTurnCount > 0 && stats.APICallCount > stats.UserTurnCount {
			cpt = float64(stats.APICallCount) / float64(stats.UserTurnCount)
		}

		// Derive perCallToolIO from actual PerTool data (chars → tokens).
		// PerTool.InputChars = tool_use JSON, PerTool.ResultChars = tool_result text.
		// Empirically measured weighted average: ~1.86 chars/token; chars/2 is the best
		// estimate without sending raw tool text to the API.
		toolIO := perCallToolIO // fallback to constant
		totalToolChars := 0
		totalToolCalls := 0
		for _, ts := range stats.PerTool {
			totalToolChars += ts.InputChars + ts.ResultChars
			totalToolCalls += ts.CallCount
		}
		if totalToolCalls > 0 {
			toolIO = totalToolChars / totalToolCalls / charsPerToken
			if toolIO < 500 {
				toolIO = 500
			}
		}

		// Derive avg response tokens from actual output data.
		avgResp := perTurnResponse // fallback
		if stats.APICallCount > 0 {
			avgResp = stats.TotalOutputTokens / stats.APICallCount
		}

		// Derive perTurnPrompt from context growth:
		//   total_growth = LastContextTokens - overhead
		//   growth_per_turn = total_growth / UserTurnCount
		//   perTurnPrompt ≈ growth_per_turn - avgResponse - toolIO*(K-1)
		prompt := perTurnPrompt // fallback
		if stats.UserTurnCount > 0 && contextToks > overheadToks {
			growthPerTurn := (contextToks - overheadToks) / stats.UserTurnCount
			toolIOPerTurn := toolIO * (int(math.Round(cpt)) - 1)
			derived := growthPerTurn - avgResp - toolIOPerTurn
			if derived > 0 {
				prompt = derived
			}
		}

		br := sessionBenchResult{
			shortID:          session.ShortID(c.entry.SessionID, 8),
			contextTokens:    contextToks,
			filteredTokens:   filteredToks,
			newContextTokens: newContextToks,
			savedPct:         savedPct,
			callsPerTurn:     cpt,
			toolIOPerCall:    toolIO,
			avgResponse:      avgResp,
			prompt:           prompt,
		}
		computeCostMetrics(&br, overheadToks, p)
		results = append(results, br)
	}

	fmt.Fprintln(out)
	if len(results) == 0 {
		fmt.Fprintln(out, "No sessions could be processed.")
		return nil
	}

	printCompressionSection(out, results)
	printCostSummary(out, results, p, *model)
	fmt.Fprintln(out)
	printWarmCostSummary(out, results, p, *model)

	return nil
}

func printCompressionSection(out io.Writer, results []sessionBenchResult) {
	fmt.Fprintln(out, "=== Compression ===")
	fmt.Fprintf(out, "%-10s  %10s  %10s  %6s\n", "Session", "Context", "NewCtx", "Saved")
	for _, r := range results {
		fmt.Fprintf(out, "%-10s  %10s  %10s  %.1f%%\n",
			r.shortID,
			analyzer.FormatNumber(r.contextTokens),
			analyzer.FormatNumber(r.newContextTokens),
			r.savedPct,
		)
	}
	fmt.Fprintln(out)

	pcts := make([]float64, len(results))
	for i, r := range results {
		pcts[i] = r.savedPct
	}
	sort.Float64s(pcts)
	mean := 0.0
	for _, p := range pcts {
		mean += p
	}
	mean /= float64(len(pcts))

	fmt.Fprintf(out, "Median: %.1f%%   Mean: %.1f%%   Range: %.1f%% — %.1f%%\n\n",
		medianFloat64(pcts), mean, pcts[0], pcts[len(pcts)-1])
}

// Cost model.
//
// Pricing: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
//
//	cache_read  = 0.1x base  (prefix matching previous cache)
//	cache_write = 1.25x base (new content written to cache)
//	input       = 1.0x base  (after last cache breakpoint — near zero with auto-caching)
//
// Multi-turn behavior (same source, "Automatic caching" table):
//
//	Request N reads [system..User(N-1)] from cache,
//	writes [Asst(N-1) + User(N)] to cache. Cross-turn write = R + P.
//
// Tool caching: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching
//
//	Client-side tools (bash/read/edit) don't get automatic server breakpoints.
//	Each client tool round-trip is a separate API call with its own cache accounting.
//
// Thinking tokens lifecycle (source: platform.claude.com/docs/en/api/messages):
//
//	output_tokens includes thinking tokens (output_tokens_details.thinking_tokens
//	provides a decomposition, but output_tokens is the authoritative billing total).
//
//	Turn N:   thinking generated         → output_tokens               @ $25/M   (Opus)
//	Turn N+1: first time as input        → cache_creation_input_tokens @ $6.25/M (Opus)
//	Turn N+2: subsequent turns as input  → cache_read_input_tokens     @ $0.50/M (Opus)
//
//	Empirically verified: thinking blocks from previous turns ARE counted as
//	input_tokens in subsequent API calls (Sonnet 4.6, 2026-06-24). The token
//	counting docs say "ignored", but the Messages API bills them — the prompt
//	caching docs ("DO count as input tokens when read from cache") are correct.
//
//	avgResponse (TotalOutputTokens / APICallCount) already includes thinking,
//	so the growth model accounts for thinking tokens accumulating in context.
//
// Per-session derived values (from real session data):
//
//	K (callsPerTurn):  APICallCount / UserTurnCount
//	toolIOPerCall:     Σ(PerTool.InputChars + ResultChars) / Σ(CallCount) / 2 chars-per-token
//	                   (empirically ~1.86 chars/token weighted average; only char counts
//	                   available — raw tool text not stored — so API call not applicable here)
//	avgResponse:       TotalOutputTokens / APICallCount (includes thinking tokens)
//
// Assumptions (not derivable from session data):
//
//	sessionOverhead:  system prompt + tool definitions + CLAUDE.md + rules.
//	                  Varies per user. Measure: open a 1-turn session, check context tokens.
//	                  Pass via --overhead flag; falls back to defaultOverhead.
//	perTurnPrompt:    average user prompt size (same for both scenarios)
const (
	defaultOverhead = 40000 // conservative default; use --overhead for your actual value
	perTurnPrompt   = 10000 // assumption: same for A and B, cancels in comparison
	perTurnResponse = 2000  // fallback when TotalOutputTokens unavailable
	perCallToolIO   = 3000  // fallback when PerTool data unavailable
	// empirically measured ~1.86 chars/token weighted avg across 16 content types
	charsPerToken = 2
)

// sessionCostParams bundles per-session derived values for cost functions.
type sessionCostParams struct {
	k             float64 // API calls per user turn (derived: APICallCount / UserTurnCount)
	toolIOPerCall int     // tokens per intra-turn API call (derived: PerTool chars / calls / 2)
	avgResponse   int     // avg output tokens per API call (derived: TotalOutputTokens / APICallCount)
	prompt        int     // avg user prompt tokens per turn (derived from context growth)
	growth        int     // cross-turn cache write = avgResponse + prompt
	overhead      int     // session overhead: system + tools + CLAUDE.md (from --overhead flag)
}

func newSessionCostParams(r *sessionBenchResult, overheadTokens int) sessionCostParams {
	g := r.avgResponse + r.prompt
	return sessionCostParams{
		k:             r.callsPerTurn,
		toolIOPerCall: r.toolIOPerCall,
		avgResponse:   r.avgResponse,
		prompt:        r.prompt,
		growth:        g,
		overhead:      overheadTokens,
	}
}

// cumulativeCostA models staying in an existing session after cache expires.
//
// Turn 1 (cache expired):
//
//	Call 1: cache write (X + P) — entire context is cache miss
//	Calls 2..K: cache read (growing prefix) + cache write (tool I/O)
//
// Turn N (N>=2):
//
//	Call 1: cache read (prefix from prev turn) + cache write (R + P)
//	Calls 2..K: cache read (growing prefix) + cache write (tool I/O)
//
// Prefix grows across turns by growth + (K-1)*toolIO per turn, because tool I/O
// from previous turns stays in the conversation history.
func cumulativeCostA(turns int, x int, sp sessionCostParams, p pricing) float64 {
	total := 0.0
	ki := int(math.Round(sp.k))
	s := sp.toolIOPerCall
	g := sp.growth
	toolIOPerTurn := s * (ki - 1)
	for n := 1; n <= turns; n++ {
		if n == 1 {
			total += float64(x+sp.prompt) * p.CacheWrite / 1e6
			for c := 2; c <= ki; c++ {
				prefix := float64(x + sp.prompt + s*(c-2))
				total += prefix*p.CachedRead/1e6 + float64(s)*p.CacheWrite/1e6
			}
		} else {
			prefixFromPrev := float64(x+sp.prompt) + float64(n-1)*float64(toolIOPerTurn) + float64(n-2)*float64(g)
			total += prefixFromPrev*p.CachedRead/1e6 + float64(g)*p.CacheWrite/1e6
			for c := 2; c <= ki; c++ {
				prefix := prefixFromPrev + float64(g) + float64(s*(c-2))
				total += prefix*p.CachedRead/1e6 + float64(s)*p.CacheWrite/1e6
			}
		}
	}
	return total
}

// cumulativeCostAWarm models staying in an existing session when cache is still warm.
//
// Cache TTL source: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
// Subscription users get a 1-hour cache TTL automatically. When you are continuously
// working within that window, the entire prefix X is already cached — Turn 1 behaves
// like Turn N≥2 in the cold model (cache read, not cache write).
//
// Turn N (all N, including N=1):
//
//	Call 1: cache read (prefix from previous turn, or X when N=1) + cache write (R + P)
//	Calls 2..K: cache read (growing prefix) + cache write (tool I/O)
func cumulativeCostAWarm(turns int, x int, sp sessionCostParams, p pricing) float64 {
	total := 0.0
	ki := int(math.Round(sp.k))
	s := sp.toolIOPerCall
	g := sp.growth
	toolIOPerTurn := s * (ki - 1)
	for n := 1; n <= turns; n++ {
		// n=1: prefixFromPrev = X (fully cached); growth shifts by 1 vs cold because
		// the previous turn's R is already in cache before our counting starts.
		prefixFromPrev := float64(x) + float64(n-1)*float64(toolIOPerTurn) + float64(n-1)*float64(g)
		total += prefixFromPrev*p.CachedRead/1e6 + float64(g)*p.CacheWrite/1e6
		for c := 2; c <= ki; c++ {
			prefix := prefixFromPrev + float64(g) + float64(s*(c-2))
			total += prefix*p.CachedRead/1e6 + float64(s)*p.CacheWrite/1e6
		}
	}
	return total
}

// cumulativeCostB models opening a new session and injecting compressed history.
//
// Setup: cache write (base) — one-time cost of injecting cc-session output.
//
// Turn 1:
//
//	Call 1: cache read (base from setup) + cache write (P)
//	Calls 2..K: cache read (growing) + cache write (tool I/O)
//
// Turn N (N>=2): same structure as A but with smaller base, cross-turn write = growth.
func cumulativeCostB(turns int, x int, c int, sp sessionCostParams, p pricing) float64 {
	base := sp.overhead + c
	total := float64(base) * p.CacheWrite / 1e6
	ki := int(math.Round(sp.k))
	s := sp.toolIOPerCall
	g := sp.growth
	toolIOPerTurn := s * (ki - 1)
	for n := 1; n <= turns; n++ {
		var prefixFromPrev float64
		var crossTurnWrite int
		if n == 1 {
			prefixFromPrev = float64(base)
			crossTurnWrite = sp.prompt // setup response negligible
		} else {
			prefixFromPrev = float64(base+sp.prompt) + float64(n-1)*float64(toolIOPerTurn) + float64(n-2)*float64(g)
			crossTurnWrite = g // R + P, same as Scenario A
		}
		total += prefixFromPrev*p.CachedRead/1e6 + float64(crossTurnWrite)*p.CacheWrite/1e6
		for c := 2; c <= ki; c++ {
			prefix := prefixFromPrev + float64(crossTurnWrite) + float64(s*(c-2))
			total += prefix*p.CachedRead/1e6 + float64(s)*p.CacheWrite/1e6
		}
	}
	return total
}

func computeCostMetrics(r *sessionBenchResult, overheadTokens int, p pricing) {
	sp := newSessionCostParams(r, overheadTokens)
	r.breakEven = -1
	for n := 1; n <= 200; n++ {
		if cumulativeCostB(n, r.contextTokens, r.filteredTokens, sp, p) < cumulativeCostA(n, r.contextTokens, sp, p) {
			r.breakEven = n
			break
		}
	}

	cost10A := cumulativeCostA(10, r.contextTokens, sp, p)
	cost10B := cumulativeCostB(10, r.contextTokens, r.filteredTokens, sp, p)
	if cost10A > 0 {
		r.saving10Pct = (cost10A - cost10B) * 100.0 / cost10A
	}

	cost100A := cumulativeCostA(100, r.contextTokens, sp, p)
	cost100B := cumulativeCostB(100, r.contextTokens, r.filteredTokens, sp, p)
	if cost100A > 0 {
		r.saving100Pct = (cost100A - cost100B) * 100.0 / cost100A
	}

	r.warmBreakEven = -1
	for n := 1; n <= 200; n++ {
		if cumulativeCostB(n, r.contextTokens, r.filteredTokens, sp, p) < cumulativeCostAWarm(n, r.contextTokens, sp, p) {
			r.warmBreakEven = n
			break
		}
	}

	cost10Warm := cumulativeCostAWarm(10, r.contextTokens, sp, p)
	if cost10Warm > 0 {
		r.warmSaving10Pct = (cost10Warm - cost10B) * 100.0 / cost10Warm
	}

	cost100Warm := cumulativeCostAWarm(100, r.contextTokens, sp, p)
	if cost100Warm > 0 {
		r.warmSaving100Pct = (cost100Warm - cost100B) * 100.0 / cost100Warm
	}
}

func printCostSummary(out io.Writer, results []sessionBenchResult, p pricing, modelName string) {
	fmt.Fprintf(out, "=== Cost Savings Per Session (%s) ===\n", modelName)
	fmt.Fprintf(out, "%-10s  %10s  %10s  %5s  %10s  %8s  %9s\n",
		"Session", "Context", "NewCtx", "K", "Break-even", "10-turn", "100-turn")

	for _, r := range results {
		beStr := "never"
		if r.breakEven > 0 {
			beStr = fmt.Sprintf("turn %d", r.breakEven)
		}
		fmt.Fprintf(out, "%-10s  %10s  %10s  %5.1f  %10s  %7.0f%%  %8.0f%%\n",
			r.shortID,
			analyzer.FormatNumber(r.contextTokens),
			analyzer.FormatNumber(r.newContextTokens),
			r.callsPerTurn,
			beStr,
			math.Round(r.saving10Pct),
			math.Round(r.saving100Pct),
		)
	}
	fmt.Fprintln(out)

	breakEvens := make([]float64, 0, len(results))
	saving10s := make([]float64, len(results))
	saving100s := make([]float64, len(results))
	for i, r := range results {
		if r.breakEven > 0 {
			breakEvens = append(breakEvens, float64(r.breakEven))
		}
		saving10s[i] = r.saving10Pct
		saving100s[i] = r.saving100Pct
	}

	sort.Float64s(breakEvens)
	sort.Float64s(saving10s)
	sort.Float64s(saving100s)

	beMedian := "never"
	if len(breakEvens) > 0 {
		beMedian = formatMedianTurn(medianFloat64(breakEvens))
	}

	fmt.Fprintf(out, "Median break-even: %s | 10-turn saving: %.0f%% | 100-turn saving: %.0f%%\n",
		beMedian,
		math.Round(medianFloat64(saving10s)),
		math.Round(medianFloat64(saving100s)),
	)
}

func printWarmCostSummary(out io.Writer, results []sessionBenchResult, p pricing, modelName string) {
	fmt.Fprintf(out, "=== Warm Cache: Cost Savings Per Session (%s) ===\n", modelName)
	fmt.Fprintf(out, "%-10s  %10s  %10s  %5s  %10s  %8s  %9s\n",
		"Session", "Context", "NewCtx", "K", "Break-even", "10-turn", "100-turn")

	for _, r := range results {
		beStr := "never"
		if r.warmBreakEven > 0 {
			beStr = fmt.Sprintf("turn %d", r.warmBreakEven)
		}
		fmt.Fprintf(out, "%-10s  %10s  %10s  %5.1f  %10s  %7.0f%%  %8.0f%%\n",
			r.shortID,
			analyzer.FormatNumber(r.contextTokens),
			analyzer.FormatNumber(r.newContextTokens),
			r.callsPerTurn,
			beStr,
			math.Round(r.warmSaving10Pct),
			math.Round(r.warmSaving100Pct),
		)
	}
	fmt.Fprintln(out)

	breakEvens := make([]float64, 0, len(results))
	saving10s := make([]float64, len(results))
	saving100s := make([]float64, len(results))
	for i, r := range results {
		if r.warmBreakEven > 0 {
			breakEvens = append(breakEvens, float64(r.warmBreakEven))
		}
		saving10s[i] = r.warmSaving10Pct
		saving100s[i] = r.warmSaving100Pct
	}

	sort.Float64s(breakEvens)
	sort.Float64s(saving10s)
	sort.Float64s(saving100s)

	beMedian := "never"
	if len(breakEvens) > 0 {
		beMedian = formatMedianTurn(medianFloat64(breakEvens))
	}

	fmt.Fprintf(out, "Median break-even: %s | 10-turn saving: %.0f%% | 100-turn saving: %.0f%%\n",
		beMedian,
		math.Round(medianFloat64(saving10s)),
		math.Round(medianFloat64(saving100s)),
	)
}

func medianFloat64(sorted []float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func formatMedianTurn(turn float64) string {
	if math.Mod(turn, 1) == 0 {
		return fmt.Sprintf("turn %.0f", turn)
	}
	return fmt.Sprintf("turn %.1f", turn)
}
