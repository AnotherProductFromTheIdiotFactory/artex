package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// The SDK injects a wrap-up/settlement prompt when an agent reaches MaxTurns or
// run_seconds/MaxDuration. It persists identified results and emits a concise
// summary instead of ending mid-work. Each agent can override the prompt through
// agents.wrapup_prompt; an empty value uses these defaults. Disabled tools and
// the settlement turn budget remain code-owned policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in defaults by agent key. Workers reuse settleWrapUpPrompt from worker.go;
// planner/mainagent have role-specific versions; custom agents use the fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// Default settlement turn budgets. A positive admin value overrides them. Ten
// turns leaves enough space to persist results; unknown agents use the generic value.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "This planning round is almost out of turns. Only this round is ending; the system will wake you again when the state changes, so the task itself is not ending. Persist conclusions already reached, but **do not manufacture intents merely for wrap-up**; zero intents remains valid. (1) If a direction should be dispatched now, submit all selected directions in one add_intent call. (2) For each goal proven by a finding or fact, call prove_goal and mark it met. (3) If you identified a serial exploit chain, record its steps with TodoWrite for the next wake-up. Then end this round without a prose summary."

const mainAgentWrapUpDefault = "This interaction is almost out of turns. Do not start new exploration or operations. **Use one standalone plain-text sentence** to summarize current progress, key conclusions, and the recommended next step for the user."

const genericWrapUpDefault = "Your run is about to end because its budget is exhausted. First persist completed results that have not yet been saved, then **use one standalone plain-text sentence** to summarize what you did and the key conclusions. That sentence will be displayed as the run result."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-timeout settlement prompts ----------
// Per-run settlement means one run exhausted its budget; task timeout means the
// entire task is ending. Their semantics can be opposite, especially for the
// planner. Only worker and planner have task-timeout prompts.

// DB overrides for task-timeout prompt and turns, wired to
// agents.task_timeout_wrapup_prompt / _max_turns for worker/planner.
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The entire task has reached its timeout and is ending.** This is not merely the budget for one run; the complete exploration is stopping. This is the final opportunity to: (1) persist **every** identified but unsaved result—new assets with insert_assets, exploration conclusions/facts with record_fact, and confirmed vulnerabilities with report_finding; (2) start no new commands or probes; and (3) **finish with one standalone plain-text sentence** summarizing the key conclusion for this intent."

const plannerTaskTimeoutDefault = "**The entire task has reached its timeout and is ending.** This is not merely the end of this round. Make one final goal judgment from **all** current facts and findings. For every goal proven by evidence, call prove_goal and mark it met. **Do not generate any new intents**, because they will not execute. Finish immediately after the judgment without a prose summary."

// TaskTimeoutWrapupDefault returns an agent's built-in task-timeout prompt.
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // Empty for unconfigured roles such as mainagent/chat.
}

// resolveTaskTimeoutWrapup prefers a nonempty DB override over the built-in
// default. Empty means this role has no task-timeout prompt; use per-run text.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // Reuse the per-run turn budget by default.
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true: Timeout uses task-timeout text; MaxTurns uses per-run text.
//   - clamped=false: both reasons use per-run text, matching wrapupSettlement.
// PromptByReason selects from the actual terminal reason at settlement time.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // Fallback and both reasons when not clamped.
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // Task deadline reached.
				harness.ReasonMaxTurns: perRun, // Run turns ended while task time remains.
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
