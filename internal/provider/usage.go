// Package provider — token usage semantics shared by backends.
//
// Zero means unreported: Ollama omits its counts on some replies and
// OpenAI may omit usage, so callers must treat 0 as "unknown", not as a
// measured zero.
package provider

// ollamaUsage builds a Usage from Ollama native counts. Total is the sum;
// all zeros means the backend reported nothing.
func ollamaUsage(prompt, completion int) Usage {
	return Usage{Prompt: prompt, Completion: completion, Total: prompt + completion}
}
