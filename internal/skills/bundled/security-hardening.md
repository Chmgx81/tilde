---
name: security-hardening
description: Review and implement secure agent, filesystem, process, dependency, secret, and approval-boundary changes.
---

# Security hardening

Map trust seams before changing code. Validate untrusted input at every seam,
fail closed for missing policy or approval, keep credentials out of prompts
and logs, and minimize tool permissions. For agent actions, enforce safety in
deterministic code rather than relying on model instructions.

Add a regression test for each confirmed finding. Run formatting, tests, static
analysis, and dependency vulnerability checks. Report residual risk honestly.
