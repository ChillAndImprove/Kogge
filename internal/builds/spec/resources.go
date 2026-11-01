// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package spec

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Resources limit a container of the container executor: its CPUs and memory (the job's own container, and each
// service). The shell executor does not limit them. Zero values set no limit.
type Resources struct {
	MilliCPUs int64 // thousandths of a CPU
	Memory    int64 // bytes
}

// Limits of resources.
const (
	MaxMilliCPUs = 1024 * 1000
	MinMemory    = 6 << 20 // the least Docker accepts
	MaxMemory    = 4 << 40
)

// CPUs formats the CPUs as the container command line takes them (--cpus 1.5).
func (r Resources) CPUs() string {
	return FormatCPUs(r.MilliCPUs)
}

// FormatCPUs formats thousandths of a CPU as a number of CPUs: 2, 0.5, 1.25.
func FormatCPUs(milli int64) string {
	s := strconv.FormatInt(milli/1000, 10)
	if frac := milli % 1000; frac != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	return s
}

// FormatMemory formats bytes as the pipeline file writes them: 4g, 512m, or a number of bytes.
func FormatMemory(b int64) string {
	for _, u := range []struct {
		suffix string
		size   int64
	}{{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}} {
		if b >= u.size && b%u.size == 0 {
			return strconv.FormatInt(b/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// ParseCPUs reads a number of CPUs such as 2, 0.5 or 1.25 (at most three decimals) as thousandths of a CPU.
func ParseCPUs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	bad := fmt.Errorf("%q is not a number of CPUs such as 2 or 0.5", s)
	if whole == "" && frac == "" || len(frac) > 3 || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return 0, bad
	}
	if len(whole) > 7 {
		return 0, fmt.Errorf("at most %d CPUs", MaxMilliCPUs/1000)
	}
	n, _ := strconv.ParseInt("0"+whole, 10, 64)
	f, _ := strconv.ParseInt((frac + "000")[:3], 10, 64)
	milli := n*1000 + f
	switch {
	case milli <= 0:
		return 0, fmt.Errorf("the number of CPUs must be more than 0")
	case milli > MaxMilliCPUs:
		return 0, fmt.Errorf("at most %d CPUs", MaxMilliCPUs/1000)
	}
	return milli, nil
}

// ParseMemory reads a size of memory such as 512m, 4g or 1.5g (k, m, g and t are powers of 1024, also written kb,
// mib and the like) as bytes.
func ParseMemory(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	bad := fmt.Errorf("%q is not a size of memory such as 512m or 4g", s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, bad
	}
	number, unit := s[:i], strings.TrimSpace(s[i:])
	var size float64
	switch strings.TrimSuffix(strings.TrimSuffix(unit, "b"), "i") {
	case "k":
		size = 1 << 10
	case "m":
		size = 1 << 20
	case "g":
		size = 1 << 30
	case "t":
		size = 1 << 40
	default:
		return 0, bad
	}
	v, err := strconv.ParseFloat(number, 64)
	if err != nil || v <= 0 {
		return 0, bad
	}
	b := v * size
	switch {
	case b < MinMemory:
		return 0, fmt.Errorf("memory must be at least 6m")
	case b > MaxMemory:
		return 0, fmt.Errorf("memory must be at most 4t")
	}
	return int64(b), nil
}

// resources reads cpus and memory over the defaults.
func (p *parser) resources(n *yaml.Node, what string, base Resources) Resources {
	r := base
	p.fields(n, what, map[string]func(*yaml.Node){
		"cpus": func(v *yaml.Node) {
			if v.Kind != yaml.ScalarNode {
				p.errorf(v, "%s.cpus must be a number such as 2 or 0.5", what)
				return
			}
			c, err := ParseCPUs(v.Value)
			if err != nil {
				p.errorf(v, "%s.cpus: %v", what, err)
				return
			}
			r.MilliCPUs = c
		},
		"memory": func(v *yaml.Node) {
			if v.Kind != yaml.ScalarNode {
				p.errorf(v, "%s.memory must be a size such as 512m or 4g", what)
				return
			}
			m, err := ParseMemory(v.Value)
			if err != nil {
				p.errorf(v, "%s.memory: %v", what, err)
				return
			}
			r.Memory = m
		},
	})
	return r
}
