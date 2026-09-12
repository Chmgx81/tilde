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

`internal/policy/slopsquatting.go` maintains a curated list of known
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
distance to detect package names that are close to well-known packages (edit
distance ≤ 2). This catches novel misspellings not yet in the database.

### Package Name Extraction

When an install command is detected, tilde extracts package names by:

1. Splitting the command into pipeline segments.
2. Identifying the package manager (`pip`, `npm`, `cargo`, ...).
3. Stripping version specs (`@1.0.0`, `>=2.0`).
4. Handling scoped packages (`@scope/pkg`).

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

The policy layer (`internal/policy/policy.go isInstallShape`) detects
install commands across package managers:

| Manager | Install Verbs |
|---------|--------------|
| pip | `install`, `download`, `wheel` |
| uv | `install`, `add`, `pip install` |
| npm | `install`, `i`, `add` |
| pnpm | `add`, `install`, `i` |
| yarn | `add` |
| bun | `add`, `install`, `i` |
| go | `get`, `install` |
| cargo | `add`, `install` |
| gem | `install` |
| bundle | `install`, `add` |
| composer | `require` |
| pipx | `install`, `run` |
| apt/dnf/yum | `install` |
| apk | `add` |
| pacman | `-S` |
| npx/uvx | (always fetch) |

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
database in `internal/policy/slopsquatting.go`. The entry should be:
- A name that is actually hallucinated (not just a rare legitimate name).
- Mapped to the real package it probably represents.
- Lowercase (matching is case-insensitive).
