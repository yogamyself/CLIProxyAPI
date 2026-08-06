package executor

import (
	"os"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// FORK PATCH (yogamyself/CLIProxyAPI) — not present upstream.
//
// Upstream cloaks every third-party tool as a virtual MCP symbol
// (mcp__<caller>__<tool>_<original>). That hides the client fingerprint, but the
// names it emits are not the ones Claude Code itself sends. This file restores
// the fork's earlier strategy — map client tools onto Claude Code's *official*
// tool names where possible — and falls back to upstream's MCP alias for
// anything that has no official equivalent.
//
// Layering it this way keeps the whole patch in one new file plus a one-line
// change at each of the two upstream alias call sites, so future upstream
// rewrites of the remap internals stay easy to merge.
//
// Toggle at runtime without rebuilding:
//
//	CPA_OFFICIAL_TOOL_CLOAKING=0   -> pure upstream behavior (MCP aliases only)
//	unset / anything else          -> official-name cloaking (this patch)
//
// The env toggle exists so the billing question that motivated this patch can be
// A/B tested against the same binary.

const officialCloakingEnvVar = "CPA_OFFICIAL_TOOL_CLOAKING"

var officialCloakingEnabled = sync.OnceValue(func() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(officialCloakingEnvVar))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
})

// officialToolNameMap maps OpenCode-style (lowercase) tool names to Claude
// Code-style (TitleCase) names. Anthropic uses tool name fingerprinting to
// detect third-party clients on OAuth traffic; presenting official names avoids
// being classified as third-party.
var officialToolNameMap = map[string]string{
	"bash":         "Bash",
	"read":         "Read",
	"write":        "Write",
	"edit":         "Edit",
	"glob":         "Glob",
	"grep":         "Grep",
	"task":         "Task",
	"webfetch":     "WebFetch",
	"todowrite":    "TodoWrite",
	"question":     "Question",
	"skill":        "Skill",
	"ls":           "LS",
	"todoread":     "TodoRead",
	"notebookedit": "NotebookEdit",
}

// officialToolNameValues is the set of names the static map can produce. A tool
// the client already sent under an official name is left untouched: it is
// indistinguishable from Claude Code on the wire, so aliasing it would only add
// a fingerprint.
var officialToolNameValues = func() map[string]bool {
	values := make(map[string]bool, len(officialToolNameMap))
	for _, name := range officialToolNameMap {
		values[name] = true
	}
	return values
}()

// dynamicOfficialToolName converts custom snake_case / kebab-case tool names to
// Claude Code-style TitleCase when the static map has no entry. This keeps names
// such as `skills_list` from exposing a third-party shape. Native MCP
// double-underscore names (mcp__server__tool) are left for the caller to handle,
// since those are already legitimate MCP traffic.
func dynamicOfficialToolName(name string) (string, bool) {
	if name == "" || strings.Contains(name, "__") {
		return "", false
	}
	if !strings.ContainsAny(name, "_-") {
		return "", false
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	if len(parts) < 2 {
		return "", false
	}
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			b.WriteString(part[1:])
		}
	}
	renamed := b.String()
	if renamed == "" || renamed == name {
		return "", false
	}
	return renamed, true
}

// officialCloakName resolves the official-looking upstream name for a client
// tool. The second return reports whether this patch wants to own the name at
// all; false means the caller should use upstream's MCP alias.
//
// A returned value equal to the input means "send unchanged" — upstream's
// rewriteName treats newName == name as no rewrite, which is exactly right for
// tools that already carry an official name.
func officialCloakName(name string) (string, bool) {
	if name == "" || !officialCloakingEnabled() {
		return "", false
	}
	if officialToolNameValues[name] {
		return name, true
	}
	if mapped, ok := officialToolNameMap[name]; ok {
		return mapped, true
	}
	return dynamicOfficialToolName(name)
}

// assignClaudeToolAlias picks the upstream-facing name for one declared client
// tool and records it in forwardMap, reserving it so no later tool can collide.
//
// It prefers an official Claude Code name and falls back to upstream's MCP
// alias — when this patch is disabled, when the tool has no official
// equivalent, or when the official name is already taken by another declared
// tool in the same request (e.g. a client that declares both `bash` and `Bash`).
func assignClaudeToolAlias(secret, name string, reservedNames map[string]bool, forwardMap map[string]string) {
	if official, ok := officialCloakName(name); ok {
		if official == name {
			// Already official on the wire; leave it alone.
			forwardMap[name] = name
			return
		}
		if !reservedNames[official] {
			forwardMap[name] = official
			reservedNames[official] = true
			return
		}
	}
	for attempt := uint32(0); ; attempt++ {
		alias := helps.ClaudeMCPToolAlias(secret, name, attempt)
		if reservedNames[alias] {
			continue
		}
		forwardMap[name] = alias
		reservedNames[alias] = true
		return
	}
}
