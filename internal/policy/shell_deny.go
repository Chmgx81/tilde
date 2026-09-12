package policy

import (
	"path"
	"strings"
)

// segmentDeny judges one argv. It's the single choke point for the
// destructive-command deny tier: every shell segment passes through here.
//
// The logic is layered:
//  1. Strip prefixes (VAR=name, shell keywords).
//  2. Detect obfuscation (glob metachars, command substitution).
//  3. Strip transparent wrappers (env, nice, timeout, command).
//  4. Detect opaque wrappers (eval, exec, sudo-family, bare shells).
//  5. Judge the underlying command by verb.
//  6. Check for dangerous redirects (tee, > /dev, > /proc, fork bombs).
func segmentDeny(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	var ok bool
	if argv, ok = stripPrefix(argv); !ok {
		return true
	}
	if len(argv) == 0 {
		return false
	}
	if isObfuscatedBinary(argv[0]) {
		return true
	}
	if hasCommandSubstitution(argv) {
		return true
	}
	first, rest, opaque := stripWrappers(argv)
	if opaque {
		return true // unparseable or opaque wrapper: fail closed
	}
	if first == "" {
		return false // exhausted in wrappers (benign)
	}
	if isDestructiveVerb(first, rest) {
		return true
	}
	if hasDangerousRedirects(argv) {
		return true
	}
	return false
}

// isObfuscatedBinary reports whether a binary name contains shell
// metachars that indicate obfuscation ($'curl', backticks, globs).
func isObfuscatedBinary(bin string) bool {
	if strings.ContainsAny(bin, "$`*?[{\\") {
		return true
	}
	if strings.ContainsAny(bin, "(){}!") || strings.HasPrefix(bin, "[[") {
		return true
	}
	return false
}

// hasCommandSubstitution reports whether argv contains $(...) or
// backtick command substitution, which hides the real command.
func hasCommandSubstitution(argv []string) bool {
	joined := strings.Join(argv, " ")
	return strings.Contains(joined, "$(") || strings.Contains(joined, "`")
}

// stripWrappers peels transparent wrappers (env, nice, timeout, command,
// bare shells) off the front of argv and returns the underlying verb plus
// its args. opaque=true means the wrapper hides the real command (opaque
// command strings, daemonizers, sudo-family, unparseable remainders) or
// leaves nothing judgeable — the caller fails such segments closed.
//
// first=="" with opaque=false means the segment was benign (e.g. a bare
// `env` that only prints): there is no command to judge.
func stripWrappers(argv []string) (first string, rest []string, opaque bool) {
	args := argv
	for len(args) > 0 {
		if name, ok := envAssign(args[0]); ok {
			switch strings.ToUpper(name) {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_PREFIX":
				return "", nil, true // git redirected outside the project
			}
			args = args[1:]
			if len(args) == 0 {
				return "", nil, true // assignments with no command
			}
			continue
		}
		first = path.Base(strings.ToLower(args[0]))
		rest = args[1:]
		switch first {
		case "env":
			rest = stripEnv(rest)
			if len(rest) == 0 {
				return "", nil, false // bare `env` prints — harmless
			}
			if len(rest[0]) > 0 && rest[0][0] == '-' {
				return "", nil, true // unjudgeable remainder
			}
		case "nice", "timeout", "command":
			rest = stripFlags(rest)
			if first == "timeout" && len(rest) > 0 && isDuration(rest[0]) {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return "", nil, true // interactive or empty
			}
		case "bash", "sh", "dash", "zsh", "ksh":
			if hasFlag(rest, "-c", "--command") {
				return first, rest, true // opaque command string
			}
			rest = stripFlags(rest)
			if len(rest) == 0 {
				return "", nil, true // bare interactive shell hangs the agent
			}
		case "eval", "exec", "sudo", "doas", "su", "setsid", "nohup", "runas":
			return first, rest, true // opaque wrapper
		default:
			return first, rest, false
		}
		args = rest
	}
	return "", nil, false
}

// isDestructiveVerb reports whether a command (first + rest) matches a
// destructive shape. Only called after wrappers are stripped.
func isDestructiveVerb(first string, rest []string) bool {
	switch first {
	case "rm":
		// Recursive removal only; plain `rm file` is Ask-tier.
		return hasFlag(rest, "-r", "-R", "-rf", "-fr", "-Rf", "--recursive")
	case "dd", "mkfs", "mkfs.ext4", "mkfs.btrfs", "shutdown", "reboot", "halt", "poweroff",
		"sudo", "doas", "su", "nc", "ncat", "netcat", "socat", "ssh":
		return true
	case "curl", "wget":
		return true
	case "scp", "rsync", "ftp", "sftp", "tftp":
		return true // exfiltration-shaped
	case "ln":
		return true // symlink/hardlink — enables link-following escapes
	case "tar", "cpio", "unzip", "gunzip", "bunzip2", "unar":
		// Extractors write wherever told — sandbox contains them, but
		// a stray -C / must never auto-run.
		for _, a := range rest {
			if a == "-C" || strings.HasPrefix(a, "--directory") {
				return true
			}
		}
		return false
	case "busybox":
		// Applets inherit the bare-verb verdict.
		for _, a := range rest {
			switch path.Base(strings.ToLower(a)) {
			case "wget", "curl", "rm", "sh", "ash", "dd", "mkfs",
				"chmod", "chown", "nc", "tar", "cpio", "unzip":
				return true
			}
		}
		return false
	case "chmod", "chown":
		return hasFlag(rest, "-R", "--recursive")
	case "find":
		return hasFlag(rest, "-delete") || hasFlag(rest, "-exec", "-execdir")
	case "xargs":
		for _, a := range rest {
			if path.Base(strings.ToLower(a)) == "rm" {
				return true
			}
		}
		return false
	case "git":
		// -C/--git-dir/--work-tree redirect git outside the project.
		for _, a := range rest {
			if a == "-C" || strings.HasPrefix(a, "--git-dir") || strings.HasPrefix(a, "--work-tree") {
				return true
			}
		}
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "push":
			return hasFlag(rest, "--force", "-f")
		case "reset":
			return hasFlag(rest, "--hard")
		case "clean":
			return hasFlag(rest, "-f", "-fd", "-df")
		case "checkout", "restore":
			for i, a := range rest {
				if a == "." {
					return true
				}
				if a == "--" && (i+1 >= len(rest) || rest[i+1] == ".") {
					return true
				}
			}
		}
		return false
	case "python", "python3", "perl", "ruby", "node", "php":
		if hasFlag(rest, "-c", "--command", "-e", "--eval", "--exec", "-r", "--require", "-M") {
			return true
		}
		if hasFlag(rest, "-m", "--module") {
			return true
		}
		joined := strings.ToLower(strings.Join(rest, " "))
		if strings.Contains(joined, "socket") || strings.Contains(joined, "urllib") {
			return true
		}
		return false
	}
	return false
}

// hasDangerousRedirects scans argv for fork bombs, absolute-path tee
// targets, and redirects at devices, /proc, or /sys.
func hasDangerousRedirects(argv []string) bool {
	for i, a := range argv {
		if strings.Contains(a, ":(){") {
			return true
		}
		target := ""
		if (a == ">" || a == ">>") && i+1 < len(argv) {
			target = argv[i+1]
		} else if len(a) > 1 && (strings.HasPrefix(a, ">") || strings.HasPrefix(a, ">>")) {
			target = a
		} else if a == "tee" || strings.HasSuffix(a, "/tee") {
			for _, t := range argv[i+1:] {
				if strings.HasPrefix(t, "-") {
					continue
				}
				if devAllowed(t) {
					continue
				}
				if strings.HasPrefix(t, "/") {
					return true
				}
			}
			continue
		}
		if isSensitiveTarget(target) && !devAllowed(target) {
			return true
		}
	}
	return false
}
