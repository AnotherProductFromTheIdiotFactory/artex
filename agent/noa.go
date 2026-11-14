package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is norma v0.4.0's experimental model-driven context compression. Users
// toggle it in system settings. It is mutually exclusive with built-in
// compaction: noaadapter.Enable installs the Compactor, Compress tool, and three
// resident prompts; omitting Enable leaves built-in compaction active. Each run
// resolves its agent's noaEnabledFn, so toggles affect later runs without rebuild.
//
// enableNoa writes persistent source archives beneath <workDir>/noa/<sessionID>.
// A globally unique session ID prevents collisions across agents. Because noa is
// experimental, initialization failure warns and falls back to built-in
// compaction without interrupting real work. On success, clear opts.Compaction
// to avoid agentcore warning that two context managers are configured.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("failed to enable noa compression; falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// noa's Compactor supersedes Compaction; clear it to prevent repeated warnings.
	opts.Compaction = nil
}
