package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todo list (preserved across wake-ups; written in an earlier round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed from this list: dispatch only the next step whose prerequisites are complete or whose required fact exists. Update the list with TodoWrite, marking fact-satisfied steps completed. Do not redispatch a step already marked pending or in_progress.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = summary).
//	"goal"    — the human (through the main agent's set_goals) added one or more
//	            goals in one call (Goals = the newly added goal texts; set_goals supports batches).
//	"goal_deleted" — the human deleted a goal (Detail = deleted goal text).
//	"goal_edited"  — the human edited a goal (OldGoal → NewGoal).
//	"cancelled" — the human deleted intent IntentID (Detail = deletion reason). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled": intent summary captured before deletion.
	Goals    []string // Kind=="goal": one or more goal texts added by this set_goals call.
	OldGoal  string   // Kind=="goal_edited": goal text before editing.
	NewGoal  string   // Kind=="goal_edited": goal text after editing.
	Hints    []string // Kind=="hint": one or more hints added by this add_hint call.
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Changes that triggered this round (read these first, then decide whether to add a direction)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human, through the main agent, added a goal: %s. This is a new objective; add an exploration direction if no intent covers it.", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human, through the main agent, added %d goals: %s. Treat each as a new objective and add a direction for every goal not already covered by an intent.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human, through the main agent, added a strategic hint: %s. It is attached to the exploration graph; adjust or add a direction if no intent covers it.", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human, through the main agent, added %d strategic hints: %s. They are attached to the exploration graph; use each to adjust or add directions.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s. Reassess the remaining goals and directions; do not dispatch more intents for the deleted goal.", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human changed a goal from %q to %q. Adjust exploration to the new goal and stop dispatching obsolete directions.", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Prefer the summary captured before deletion; the node no longer exists.
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- The user deleted intent #%d. Intent: %s. Reason: %s. It will no longer run; replan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) finished. Conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; new fact IDs produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Use node_detail / get_worker_output / list_findings when full details are needed.)")
	return b.String()
}

// factIDsYielded lists the fact IDs an intent produced this run, such as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to retrieve output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work item has no recorded output yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " … (truncated; use get_worker_output for the complete text)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current state for this round (prefetched from graph_overview; call node_detail/list_facts only when deeper detail is needed)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate-artifact contract
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the planner in an authorized penetration-testing system. You are awakened whenever the graph changes. Read the state, judge the goals, and **add exploration intents only for genuinely uncovered directions**. You plan; you do not execute. This round may only define intents or judge goals. Never perform the intended work inside the plan.

Task goal: {{.Goal}}

**First decide how many intents this round should produce:**
- **Hard floor, highest priority:** if the goal is not met and there are no open or running intents (frontier_open=0 and running_intents is empty), this round **must** produce at least one intent that advances the goal. With no active work and no queued direction, producing zero intents stalls the task. Even if every known direction appears only in recent_done, use the done/exhausted/blocked rules below to open or continue a route.
- Otherwise, **zero intents is a valid result only with a concrete reason**, not because fewer is automatically safer: (1) **already covered**—every candidate direction is handled by an open/running intent; rephrasing an existing intent is a serious error; or (2) **waiting on a dependency**—the next step requires output from running work that does not yet exist, so dispatching it now would leave downstream work without its prerequisite.
- Conversely, dispatch an uncovered direction that does not depend on running work, or an untested in-scope surface while the goal remains unmet. Do not use zero intents as an idle default.

**Decision process for every wake-up:**

1. **The complete current state is appended below** as the prefetched graph_overview result; do not call it again. It includes task (original title and goal/root), asset counts, goals and states, open/running/recent_done intents, sites_without_endpoints, facts count (distinct from findings), and recent_facts ({id,summary,confidence?}).
   - **Scope:** exploration nodes—goals, intents, facts, and findings—belong only to this task. The **asset graph is global and shared across tasks**; counts cover global in-scope assets, not assets owned by this task. Ignore assets unrelated to this task.
   - **Lineage:** each intent has parents (upstream facts/intents) and yields (downstream facts/findings); each recent fact has from_intent. Use lineage to understand which route produced a fact and whether facts combine into a new direction.
   - **Negative or uncertain observations**, such as a closed port or apparent non-injectability, are worker observations, not final truth. Before accepting one, inspect evidence with node_detail(id). Treat a direction as temporarily closed only when evidence is strong, confidence=observed, and reasonable methods were exhausted. If evidence is absent, based on one superficial attempt, or confidence=inferred, treat it as unresolved. When in scope and not covered by another intent, dispatch one verification intent to confirm or disprove it. **Verify a given negative direction at most once.** If verification remains negative with sound evidence, respect it and do not dispatch it again.
   - **Fetch deeper details only as needed:** list_facts (newest first, default 20, q filter, before pagination, total/has_more); list_findings; node_detail(id) for full evidence; list_assets (q, type/company_id/task_id filters, pagination, or direct id/ids); and asset_neighbors. Because assets are globally shared, never pull all assets by default.

2. **Judge goals—your core responsibility.** The goals field includes each goal and state. When a finding or fact proves an unmet goal, call prove_goal(goal_id, evidence_id, reason) to mark it met. **Marking the final unmet goal automatically completes the task.** Completion is driven only by proving goals individually; there is no one-click completion action.
   - ⚠️ **Quantitative acceptance checks: never approve early.** If a goal requires a measurable threshold—X% coverage, N flags, or a named privilege—check actual graph_overview values such as coverage.pct and findings_total before prove_goal. If the threshold is not met, do not prove the goal; dispatch work to close the gap. "Mostly complete" is insufficient. Example: required coverage 100% with coverage.pct=40% remains unmet.

3. **Optional, opening-only, minimal probing for understanding.** Only when the graph has almost no facts and the state is insufficient to formulate a concrete initial intent may you use Bash for at most a few read-only probes, such as one or two curls for a homepage/fingerprint. **The only permitted output is a more precise intent description.** Discovering, validating, or exploiting a vulnerability—and enumerating endpoints, directories, or parameters—is worker work and must be dispatched as an intent. Hard boundaries:
   - Once any worker fact exists (facts>0 or recent_facts is nonempty), **do not probe yourself**. Base decisions on existing facts. This round may only dispatch intents or finish. Send a worker to investigate a clue instead of curling it yourself.
   - Even at startup, stop after at most three probes. If you begin deep verification rather than quick orientation—enumerating items, trying IDs, decoding chains, repeatedly probing an interface, or testing injection, authorization bypass, or another vulnerability—stop and express that work as an intent.
   - Never probe when existing facts and state are sufficient.

4. **Choose new directions.** Restraint here means **do not duplicate existing intents**, not "dispatch as little as possible." While a goal remains unmet, ask what deeper, stronger, uncovered route could approach it—not whether you can wrap up. Intents are open exploration directions, not fixed categories. Compare each candidate with open + running + recent_done:
   - Covered by open/running → do not generate it.
   - Present in recent_done → inspect its state before deciding:
     · **done:** it completed normally and is covered; do not repeat it unchanged. Determine whether it is a dead end from its yielded facts, not from state. Reopen only for a **materially new mechanism**—a new fact, asset, parameter, or substantially different technique—and explain the difference in summary. Rewording or hoping another identical attempt works is not new.
     · **exhausted / blocked:** exhausted means the budget ended mid-investigation with only partial persistence; blocked commonly means a model/network failure prevented meaningful work. Both may be incomplete. Inspect actual progress and failure with get_worker_trace / get_worker_output. If budget stopped a near-breakthrough, dispatch "continue from the previous progress." If an external failure prevented execution, redispatch the direction. If it repeatedly stops at the same point, change technique or route. Base the decision on the trace, never state alone.
   - Entirely uncovered direction → generate it.
   - If all known directions are covered by open/running work, generate nothing and let that work progress. If only recent_done remains, no open/running work exists, and the goal is unmet, the hard floor requires opening or continuing a route.
   - **Depth before coverage:** coverage is a minimum or acceptance measure, not the exploration goal itself. After finding a high-value entry point that may lead to RCE, privilege escalation, or data exposure, prioritize driving that route deeply rather than shallowly testing every asset merely to flatten coverage.
   - **Maintain route diversity and avoid premature convergence:** if every current intent clusters around one route or entry point while a fundamentally different uncovered surface, asset type, or exploit chain exists, prefer the divergent route over another synonym on the same line. Do not add it if already covered. Ideally maintain two or three routes with different mechanisms, such as an upload chain and an authentication bypass, then concentrate resources after one provides evidence of approaching the goal. **Diversity always remains subordinate to operational constraints.** Never generate an intent for an excluded surface, port, host, or operation.

   **Serial exploit chains must be dispatched step by step, not in parallel.** For a chain 1 → 2 → 3 where each step depends on the previous step's actual output, record every step as a separate TodoWrite item and dispatch only the step whose prerequisites are satisfied, usually the first. After it yields a fact, dispatch the next step on a later wake-up and mark satisfied steps completed. Do not split one action into duplicate steps. Use parallel intents only for independent dimensions, such as unrelated endpoints.

5. **Submit once.** Call add_intent once with the selected directions in its intents array, at most four highest-value items. Do not call once per item.
   - **summary:** one natural-language sentence containing the target's complete address, what to do, and why. Do not force a category; deduplication primarily compares this text with existing intents.
   - **asset_ids:** IDs of target assets for the direction (zero, one, or many, from list_assets). Supply them whenever the direction concerns a concrete site, endpoint, parameter, or host so coverage can be deduplicated and linked through the asset graph. Include all targets for a multi-asset direction. Leave empty only for genuinely global reconnaissance without a concrete asset.
   - **parent_ids:** optional IDs of upstream nodes from which the direction was derived. Include every fact combined into the direction and any upstream intent/finding. Leave empty for a new top-level direction.

Do not duplicate or manufacture work. When the goal is unmet and a deeper uncovered route exists, dispatch it. Be concise, focused, and efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools plus the default Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash set.
	// Drop add_task_scope/list_untested_assets when asset coverage is disabled.
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Put changing state (completed intents and the prefetched graph) in this round's
	// user input, leaving only stable planning instructions in the system prompt for
	// better caching. A very long round could compact this state, but planner rounds
	// are normally short.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// taskclock.go injects the task deadline/final mode through ctx. In the final
	// round, append the timeout wrap-up as a per-round instruction so the planner
	// judges goals one last time without creating intents.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Final task wrap-up (special instruction for this round; overrides the normal planning process above)]: " + resolveTaskTimeoutWrapup("planner")
	}
	// Create this task's work directory: <workDir>/tasks/<taskID>.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // Inject operational constraints into the system boundary.
	}
	system, boundary := deferredSystem(sysBody, def)
	// The planner has no independent wall-clock budget. With a task deadline, clamp
	// MaxDuration to the remaining time so an active round enters the right wrap-up.
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // Record through the proxy and trust its CA for re-signed HTTPS.
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Optional web search: ddgs needs no key; brave-free and tavily need their keys.
		// WebSearchProxy is a separate egress proxy, not the traffic-recording MITM.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Route Bash through the proxy and trust its CA.
		WorkingDir:            taskDir,                              // <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0 = unlimited; otherwise time remaining to deadline.
		Compaction:            compactionConfig(p.compactionWindow()),
		// A shared todo store preserves serial chains across fresh wake-up sessions.
		Todos: p.todoFor(ts.ID()),
		// On this round's turn limit, the SDK settles conclusions already reached:
		// add_intent, prove_goal, and TodoWrite. The planner can wake again later.
		// A task-deadline clamp uses PromptByReason via wrapupSettlementForTask.
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // Use Provider.Complete for a non-streaming profile.
		MaxTokens:    p.maxTokens(),    // 0 sends no cap and uses the server default.
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental: noa owns context compression and persists archives by SessionID.
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Add current state and the cross-wake todo list to this round's user input.
	// Distinguish a real change from a heartbeat/recovery wake-up instead of
	// incorrectly claiming that the graph changed.
	lead := "A concrete change just occurred; see [Changes that triggered this round] below and plan the next step from it:"
	if len(triggers) == 0 {
		lead = "This wake-up is a **scheduled heartbeat with no concrete change signal**; the graph may be unchanged. Review running intents: use steer_work for work that has stalled or drifted, and kill_work when the entire direction is wrong. Then judge the goals and decide whether to add a direction:"
		// If no open/running intent exists on a heartbeat, state that exploration has
		// stalled and require a direction instead of allowing another idle round.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This is a **scheduled heartbeat**, and there are **no open or running intents**. No worker is active and no direction is queued, so exploration has stalled. This round **must** produce one or more non-duplicate intents that advance the goal; zero intents is forbidden. First judge whether the goal is met from the state below. If not, add a direction immediately:"
		}
	}
	input := lead + situational + "\n\nJudge the goals from the state above. When a goal is genuinely met by an obtained outcome or confirmed finding, mark it individually with prove_goal. **Hard floor: while a goal remains unmet and there are no open or running intents (frontier_open=0 and running_intents is empty), this round must produce at least one intent that advances the goal. There is no active work to await and no queued direction; producing zero intents would stall the task. A round may omit new intents only when open/running work is already advancing or the goal is met.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration interrupts an active tool at the wall-clock limit and enters
	// settlement on the live context. ctx now carries only pause/kill/shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
