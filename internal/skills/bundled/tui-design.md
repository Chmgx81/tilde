---
name: tui-design
description: Design and audit tilde terminal flows for clarity, keyboard control, state visibility, accessibility, and calm feedback.
---

# TUI design

Prioritize the current task, output, and recovery action over chrome. Every
long operation needs visible progress; every mode needs an explicit status;
every destructive action needs confirmation or undo. Keep stdout data
composable, use stderr for diagnostics, support keyboard-only operation, and
respect narrow terminals and reduced-color environments.

Test the full flow: startup, input, processing, tool approval, failure,
completion, cancellation, resize, scroll, copy, and resume.
