// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"regexp"
	"strings"

	"kogge.de/kogge/internal/docmodel"
)

// Command is a commit command: "SHOP-12 #comment Fixed rounding #time 1h 30m #resolve-issue".
type Command struct {
	Keys []string // the issue keys the command applies to
	Name string   // comment, time or a transition name in lower case with "-" for spaces
	Args string
}

var commandPattern = regexp.MustCompile(`^#([a-z][a-z0-9-]*)$`)

// ParseCommands finds the commands of a commit message. A line names issue keys and then commands; each command
// takes the words up to the next command as its arguments.
func ParseCommands(message string) []Command {
	var out []Command
	for _, line := range strings.Split(message, "\n") {
		words := strings.Fields(line)
		var keys []string
		var cur *Command
		for _, w := range words {
			if m := commandPattern.FindStringSubmatch(strings.ToLower(w)); m != nil && len(keys) > 0 {
				if cur != nil {
					out = append(out, *cur)
				}
				cur = &Command{Keys: keys, Name: m[1]}
				continue
			}
			if cur == nil {
				if k := strings.TrimRight(w, ".,:;"); docmodel.IsIssueKey(k) {
					keys = append(keys, k)
				}
				continue
			}
			if cur.Args != "" {
				cur.Args += " "
			}
			cur.Args += w
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	return out
}

var durationWord = regexp.MustCompile(`^\d+(\.\d+)?[wdhm]$`)

// splitTime separates "1h 30m fixed it" into the duration and the comment.
func splitTime(args string) (string, string) {
	words := strings.Fields(args)
	i := 0
	for i < len(words) && durationWord.MatchString(words[i]) {
		i++
	}
	return strings.Join(words[:i], " "), strings.Join(words[i:], " ")
}
