// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The analysis and validation reports are read by the administrators of the customer, in English or German.
// Their texts follow the approach of Kogge's error messages (internal/platform/apperr): worded
// in English in the code, German in messages_de.json keyed by the English text before formatting; a test keeps
// the catalog complete. Values go into the German wording in their order, or as %[n]s where German needs another.

//go:embed messages_de.json
var messagesDE []byte

var catalogDE = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(messagesDE, &m); err != nil {
		panic("importer: messages_de.json: " + err.Error())
	}
	return m
}()

// Languages of the reports.
const (
	English = "en"
	German  = "de"
)

// lang words the texts of a report.
type lang string

// ParseLanguage checks the language of a report.
func ParseLanguage(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", English:
		return English, nil
	case German:
		return German, nil
	}
	return "", fmt.Errorf("the language %q is not available (de, en)", s)
}

// t words a text; format is the English text with its verbs.
func (l lang) t(format string, args ...any) string {
	if l == German {
		if de, ok := catalogDE[format]; ok {
			format = de
		}
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// n formats a number with the thousands separator of the language.
func (l lang) n(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	sep := ","
	if l == German {
		sep = "."
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// bytes formats a size.
func (l lang) bytes(v int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(v)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if i == 0 {
		s = strconv.FormatInt(v, 10)
	}
	if l == German {
		s = strings.ReplaceAll(s, ".", ",")
	}
	return s + " " + units[i]
}

// duration formats a duration in hours and minutes.
func (l lang) duration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int64(d/time.Hour), int64(d%time.Hour/time.Minute)
	switch {
	case h > 0:
		return l.t("%d h %d min", h, m)
	case m > 0:
		return l.t("%d min", m)
	}
	return l.t("less than a minute")
}

// time formats a moment.
func (l lang) time(t time.Time) string {
	if t.IsZero() {
		return "–"
	}
	if l == German {
		return t.Local().Format("02.01.2006 15:04 MST")
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}
