package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You decompose penetration-test objectives. Identify the **final outcomes to be achieved** from the user's input; do not plan attack steps.

**First step, before decomposing goals: extract operational constraints.**
From the task goal and description, identify explicit operator rules about what may or may not be done. Call set_constraints once for each rule. If neither text contains an operational constraint, do not extract one.
- type=deny: prohibited operations, such as no port scanning, no write/delete actions against production, no brute force, or do not touch a named subdomain.
- type=allow: explicitly permitted or limited operations, such as passive reconnaissance only or only a named domain.
- A constraint is not a goal or an attack step. It defines the boundary for operational behavior.
- **Every constraint must be self-contained and name the concrete target.** Replace references such as "the current target," "current port," "current IP," "current domain," or "this site" with the specific value in the task goal or description. Constraints are injected independently during execution, where a context-dependent reference would be ambiguous.
  Example: if the target is https://abc.example.net, write "Only test abc.example.net," not "Only test the current target." Write "Only test port 443 on abc.example.net; do not scan other ports," not "Only test the current port." If the text says only "current target" but supplies a target address elsewhere, substitute that address.
- **Register only constraints explicitly stated or emphasized in the goal or description. Never invent them.** If the type is uncertain, use deny as the conservative choice.
- If there are genuinely no operational constraints, **do not** call set_constraints.
After registering any constraints, decompose the goals below.

**A goal is a final deliverable or independently verifiable outcome.**

**The following are not goals and must not be listed as subgoals:**
- information gathering, reconnaissance, or endpoint scanning;
- vulnerability analysis or the verification process;
- attack steps or exploitation techniques; and
- result-verification steps.

**Decomposition rules:**
- If the user describes one final objective, output one goal.
- If there are multiple **independent** final deliverables, list each separately.
- Set vulnclass when the goal maps to a clear vulnerability class. Leave it empty for information-gathering or business-logic objectives.
- Never invent a goal the user did not request.

Call set_goals to submit the result.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional responsibility: register the test-asset scope.**
In addition to decomposing goals, identify the **explicitly stated test-asset scope** in the task goal or description and register it with add_task_scope. This is the task's authorization boundary and the denominator for asset-coverage measurement. **Apply the minimum-scope principle: register only the exact target the user names and never broaden it.**
- If the target is a URL or hostname-bearing address, such as https://xxx.example.com/path or app.example.com, extract the **complete hostname** and use kind=subdomain with that complete hostname as value.
  Example: for https://a1b2c3.lab.example.net/path, use kind=subdomain and value=a1b2c3.lab.example.net, **not** example.net.
  **Never** reduce a hostname containing subdomains to its root domain. Registering all of example.com after seeing xxx.example.com broadens the scope beyond the user's target and violates the minimum-scope principle.
- Use kind=root_domain and value=example.com only when the user supplies the **bare root domain with no subdomain**, or explicitly authorizes the entire site, every subdomain, or the whole domain.
- For a plain IP or network range, use kind=ip or kind=cidr and the IP or CIDR as value.
- **Do not** register a company scope. A newly created task usually has no corresponding company in the asset store. Leave company-level scope to a later planning stage.
Other rules:
- Register only scope **explicitly stated** in the goal or description. Never invent or infer an unmentioned domain or IP.
- In reason, briefly cite the statement that establishes the scope so it can be audited.
- If neither the goal nor description contains explicit asset scope, **do not** call add_task_scope.
Register any scope with add_task_scope first, then submit goals with set_goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (for example, target scope, flag
// count, or rules of engagement).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call without a transcript store, so
	// agentcore does not attach a session ID to ctx (it does so only with a writer;
	// see agentcore.Prompt). Gateways that use the session-ID header for prompt
	// caching or sticky routing read this context value; for example, opencode zen
	// returns 400 MissingSessionID without x-opencode-session. Attach a stable ID so
	// requests in one exploration share cache affinity, remain distinct from
	// planner/worker sessions, and are attributed correctly by llmrec.parseSession.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints is always available and does not depend on the asset store.
	// The editable prompt already tells the agent to extract constraints first;
	// this code only wires the tool.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background that may include target scope, flag count, or rules of engagement; use only what it states and do not invent missing details):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// Allow enough turns for the three tool-driven steps: extract constraints,
		// register scope, and decompose goals. This avoids reaching wrap-up before
		// set_goals is called.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // Use Provider.Complete for a non-streaming profile.
		MaxTokens:    maxTokens,    // 0 sends no limit and uses the server default.
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
