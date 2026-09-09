# Vision design

> Acceptance contract for image-aware sessions.

Vision is not implemented yet. Tilde currently accepts text prompts only; its
provider `Message` interface has no image parts and the TUI has no image
attachment state. This document is the acceptance contract for the feature so
it is not accidentally represented as supported.

**Current status:** `web_shot` can create a screenshot artifact for human
review, but tilde does not yet send image bytes through the provider or invoke
a vision fallback. Keep this limitation visible until the implementation and
tests below land.

## Contract

- A user may attach an image or name an image path in the latest prompt.
- Native vision models receive the image through their normal provider request.
- Non-vision models may use one explicit `vision` side-call to a configured,
  vision-capable provider without changing the main session provider/model.
- The side-call is opt-in, approval-gated on first use, time-bounded, and
  visible in the TUI as `VISION` with success or failure details.
- Only images from the latest user message are available as attachments.
- Image paths are resolved inside the project boundary; symlinks, directories,
  unsupported formats, and oversized files are rejected.
- Image bytes never enter logs, prompts, or error messages. Only the bounded
  model-generated description is retained, with output fenced as untrusted data.
- Failure is diagnostic and non-fatal: the turn continues without vision and
  tells the user how to retry, enable the feature, or switch models.

## Required seams and tests

1. `provider.Message` image parts plus provider capability reporting.
2. TUI attachment state, latest-message expiry, and `/config` settings.
3. A `vision.Reader` interface with a deterministic fake for tests.
4. Path validation and MIME/size limits tested independently.
5. OpenAI-compatible, Ollama, Anthropic, and Gemini wire-format tests where
   each backend supports image input.
6. Approval, timeout, failure-open, prompt-injection fencing, and redaction
   tests.
7. An end-to-end test proving the main provider/model does not change.

The implementation must not claim universal vision support: capability is
provider/model-specific and must be surfaced honestly.
