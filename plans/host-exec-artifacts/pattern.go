package main

import (
	"bufio"
	"os"
	"strings"
)

// Pattern: whitespace-split tokens. A final "*" matches all remaining args.
// A mid-pattern "*" matches exactly one token.
type Pattern struct {
	Raw          string
	Tokens       []string // command + fixed arg tokens (excluding trailing *)
	TrailingStar bool
}

// Match reports whether (cmd, args) satisfies this pattern.
func (p Pattern) Match(cmd string, args []string) bool {
	if len(p.Tokens) == 0 {
		return false
	}
	if p.Tokens[0] != cmd {
		return false
	}
	patArgs := p.Tokens[1:]
	if p.TrailingStar {
		if len(args) < len(patArgs) {
			return false
		}
		for i, t := range patArgs {
			if t == "*" {
				continue
			}
			if args[i] != t {
				return false
			}
		}
		return true
	}
	if len(args) != len(patArgs) {
		return false
	}
	for i, t := range patArgs {
		if t == "*" {
			continue
		}
		if args[i] != t {
			return false
		}
	}
	return true
}

type Ruleset struct {
	Patterns []Pattern
}

// Match returns (true, rawPattern) on first hit; (false, "") otherwise.
func (rs *Ruleset) Match(cmd string, args []string) (bool, string) {
	for _, p := range rs.Patterns {
		if p.Match(cmd, args) {
			return true, p.Raw
		}
	}
	return false, ""
}

func LoadRuleset(path string) (*Ruleset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rs := &Ruleset{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		toks := strings.Fields(line)
		if len(toks) == 0 {
			continue
		}
		p := Pattern{Raw: line}
		if toks[len(toks)-1] == "*" {
			p.TrailingStar = true
			p.Tokens = toks[:len(toks)-1]
		} else {
			p.Tokens = toks
		}
		if len(p.Tokens) == 0 {
			// Pattern was just "*" alone — reject, too permissive.
			continue
		}
		rs.Patterns = append(rs.Patterns, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rs, nil
}
