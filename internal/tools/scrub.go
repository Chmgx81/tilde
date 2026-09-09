package tools

// Scrub redacts secrets via the shared leaf package internal/scrub — the
// single source of truth for secret patterns (see that package's header:
// tools cannot be imported by hooks, so the patterns live in a leaf).
// These thin wrappers keep existing callers (session, audit, export)
// working unchanged while guaranteeing one pattern set everywhere.

import "tilde/internal/scrub"

func Scrub(s string) (string, int) { return scrub.Scrub(s) }

func IsHighRiskPath(p string) bool { return scrub.IsHighRiskPath(p) }

func AnnotateHighRisk(p string) string { return scrub.AnnotateHighRisk(p) }
