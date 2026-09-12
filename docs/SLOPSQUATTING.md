# Slopsquatting Defense

"Slopsquatting" is a portmanteau of "slop" (LLM-generated content) and
"typosquatting" (registering misspelled package names). It describes an
attack where:

1. An LLM frequently hallucinates plausible-but-fake package names (e.g.
   `langchin` for `langchain`, `reque-sts` for `requests`).
2. An attacker pre-registers those names on public registries (npm, PyPI,
   crates.io) with malicious code.
3. A user approves the model's suggestion to install the package.
4. The malicious code runs on the user's machine.

tilde defends against slopsquatting at multiple layers.

## Detection

### Hallucination Database

`internal/slopsquatting/slopsquatting.go` maintains a curated list of known
hallucinated package names across ecosystems (Python, JavaScript, Rust, Go).
Each entry maps the hallucinated name to the real package it probably
represents:

```go
var hallucinations = map[string]string{
    "langchin":   "langchain",
    "reque-sts":  "requests",
    "numpyy":     "numpy",
    // ... 50+ entries
}
```

The database is intentionally conservative: a name is only listed if it is
both a common hallucination AND a realistic attack vector.

### Typo Detection

In addition to the known-hallucination database, tilde uses Levenshtein
distance to detect package names close to a curated set of well-known
packages, catching novel misspellings not yet in the database. Matching is
case- and separator-insensitive (`Lang-Chin` == `langchin`), and the
distance is length-aware: names under five characters only flag at edit
distance 1, which keeps short real names from triggering false "possible
typo" warnings. A name that is itself a known package is never flagged.

### Package Name Extraction

When an install command is detected, tilde extracts package names by:

1. Joining backslash-newline continuations, then splitting into pipeline
   segments and peeling transparent wrappers (`env`, `nice`, `timeout`,
   `command`) and `VAR=x` prefixes, so the manager can't hide.
2. Identifying the package manager, including versioned binaries
   (`python3.11`, `pip3.11`) and the `python -m pip` indirection.
3. Stripping version specs (`==1.2`, `~=`, `<`, `!=`, `[extra]`, `@scope/pkg@1.0`).
4. Handling scoped packages (`@scope/pkg`).
5. Skipping flag values (`-r requirements.txt`, `--index-url URL`) instead
   of reading them as package names, and dropping unresolved `$VAR` tokens.

A package name that resolves to a known package renders as
`known package(s)`; anything else keeps the `unverified package name`
notice.

## Warning Prompts

When a hallucinated package name is detected in an install command, the
confirmation prompt shows a prominent warning:

```
Run: pip install langchin — ⚠ SLOPSQUATTING WARNING —
langchin (did you mean langchain?) — verify on the registry before approving
```

For multiple suspicious packages:

```
Run: pip install langchin requsts — ⚠ SLOPSQUATTING WARNING —
multiple suspicious packages: langchin (did you mean langchain?);
requsts (did you mean requests?) — verify on the registry before approving
```

## Strict Mode

Setting `TILDE_STRICT_INSTALL=1` makes all install commands require explicit
user confirmation, **even in Auto mode with `--yes`**. This is the
recommended setting for security-conscious users:

```sh
export TILDE_STRICT_INSTALL=1
tilde
```

In strict mode:
- Non-install commands still auto-approve in Auto mode.
- Install commands always prompt.
- The `[a]` (approve always) key still works for specific commands.

## Install Shape Detection

The policy layer (`internal/policy/policy.go`) detects install commands
across package managers from **one table** shared by detection and name
extraction, so a manager that is detected is always also DB-checked:

| Manager | Fetch verbs |
|---------|-------------|
| pip / pip3 | `install`, `download`, `wheel` |
| python / python3 | `-m pip` + a pip verb |
| uv | `install`, `add`, `sync` |
| poetry | `add`, `install` |
| pipenv / pdm | `install`, `add`, `sync` |
| conda / mamba | `install`, `create` |
| npm | `install`, `i`, `add`, `ci` |
| pnpm | `add`, `install`, `i`, `dlx` |
| yarn | `add`, `install`, `dlx` |
| bun | `add`, `install`, `i`, `x` |
| go | `get`, `install` |
| cargo | `add`, `install` |
| gem / bundle | `install`, `add` |
| composer | `require`, `install` |
| apt/apt-get/dnf/yum/brew/choco | `install` |
| apk | `add`, `install` |
| pacman | `-S`, `-Syu`, `--sync`, `install` |
| pipx | `install`, `run` |
| deno | `install`, `add` |
| dotnet / nuget | `add` / `install` |
| npx / uvx / bunx | (always fetch) |

Multi-segment commands are also caught: `cd proj && pip install x` flags
the install even though it's not the first segment.

## Verifying Packages

Before approving an install of an unfamiliar package, verify it:

- **PyPI**: `https://pypi.org/project/<name>/`
- **npm**: `https://www.npmjs.com/package/<name>`
- **crates.io**: `https://crates.io/crates/<name>`

Check:
- Download count (popular packages have many)
- Recent activity (abandoned packages are riskier)
- Repository link (legitimate packages link to source)
- Author/maintainer (known entities are safer)

## Limitations

Slopsquatting detection in tilde is **defense in depth**, not a guarantee:

- New hallucinations may not be in the database yet.
- The typo detector has false positives (legitimate packages that happen to
  be close to well-known ones).
- Determined attackers can register names that are not yet in the database.

The mitigation is **user confirmation**: every install of an unfamiliar or
suspicious package requires explicit approval. The warning makes the risk
visible; the user makes the decision.

## Contributing

If you discover a new common hallucination, consider adding it to the
database in `internal/slopsquatting/slopsquatting.go`. The entry should be:
- A name that is actually hallucinated (not just a rare legitimate name).
- Mapped to the real package it probably represents.
- Lowercase (matching is case-insensitive).
