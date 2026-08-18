package remote

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// maxIncludeDepth stops an Include cycle. Real configurations nest one or
// two deep; anything past this is a loop, and following it forever is a
// dashboard that never opens its own modal.
const maxIncludeDepth = 8

// ConfigHost is one machine named in the user's SSH configuration.
type ConfigHost struct {
	// Name is the alias, and what Stormlight calls the machine.
	Name string
	// HostName, User, and Port are what ssh would use for it, when the
	// configuration says. Empty means ssh's own default — for HostName
	// that is the alias itself.
	HostName string
	User     string
	Port     string
}

// Summary is the machine in one phrase, for a list that has to say which
// `builder` this is. It says nothing when the configuration adds nothing
// to the name, rather than repeating it back.
func (h ConfigHost) Summary() string {
	hostName := h.HostName
	if hostName == "" {
		hostName = h.Name
	}
	summary := hostName
	if h.User != "" {
		summary = h.User + "@" + hostName
	}
	if h.Port != "" && h.Port != "22" {
		summary += ":" + h.Port
	}
	if summary == h.Name {
		return ""
	}
	return summary
}

// KnownHosts reads the host names out of the user's SSH configuration, in
// the order they appear, as suggestions for a machine to work on.
//
// They are suggestions rather than a roster: Stormlight can reach any
// destination ssh can, whether or not it is named in a file. Plenty of
// people have no ssh config at all — this returns nothing for them, and
// typing a destination has to work just as well.
//
// Patterns are skipped. `Host *.internal` and `Host build-*` configure
// whatever matches them; neither is a machine anyone can connect to, so
// offering them as choices would be offering something that cannot work.
// A negated pattern (`!prod`) is exclusion, not a name.
func KnownHosts() []ConfigHost {
	return knownHostsFrom(defaultSSHConfigPath(), 0)
}

func knownHostsFrom(path string, depth int) []ConfigHost {
	if depth > maxIncludeDepth || strings.TrimSpace(path) == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		// No configuration is the ordinary case, not a failure.
		return nil
	}
	defer file.Close()

	var hosts []ConfigHost
	// Directives belong to the Host block above them, and one block can
	// name several machines — `Host build ci` configures both.
	var current []int
	add := func(host ConfigHost) int {
		for index, known := range hosts {
			if known.Name == host.Name {
				return index
			}
		}
		hosts = append(hosts, host)
		return len(hosts) - 1
	}
	set := func(apply func(*ConfigHost)) {
		for _, index := range current {
			apply(&hosts[index])
		}
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		keyword, rest, ok := splitDirective(scanner.Text())
		if !ok {
			continue
		}
		switch keyword {
		case "host":
			current = nil
			for _, pattern := range strings.Fields(rest) {
				if isHostPattern(pattern) {
					continue
				}
				current = append(current, add(ConfigHost{Name: pattern}))
			}
		case "hostname":
			set(func(host *ConfigHost) { host.HostName = rest })
		case "user":
			set(func(host *ConfigHost) { host.User = rest })
		case "port":
			set(func(host *ConfigHost) { host.Port = rest })
		case "include":
			for _, included := range strings.Fields(rest) {
				for _, match := range expandInclude(included) {
					for _, host := range knownHostsFrom(match, depth+1) {
						add(host)
					}
				}
			}
		}
	}
	return hosts
}

// splitDirective reads one configuration line. ssh accepts `Keyword value`
// and `Keyword = value`, and is case-insensitive about the keyword.
func splitDirective(line string) (keyword, rest string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.ReplaceAll(line, "=", " ")
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", false
	}
	return strings.ToLower(fields[0]), strings.Join(fields[1:], " "), true
}

func isHostPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?") || strings.HasPrefix(pattern, "!")
}

// expandInclude resolves an Include path the way ssh does: relative paths
// are relative to ~/.ssh, and globs are allowed.
func expandInclude(pattern string) []string {
	pattern = strings.Trim(pattern, `"`)
	if strings.HasPrefix(pattern, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		pattern = filepath.Join(home, pattern[2:])
	}
	if !filepath.IsAbs(pattern) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		pattern = filepath.Join(home, ".ssh", pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

func defaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}
