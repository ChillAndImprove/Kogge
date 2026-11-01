// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package apperr

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Errors are worded in English in the code; their German wording is in messages_de.json, keyed by the English
// message before formatting (a test keeps it complete). The values of a message go into the German one in their
// order, or as %[n]s where German needs another order.

//go:embed messages_de.json
var messagesDE []byte

var catalogDE = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(messagesDE, &m); err != nil {
		panic("apperr: messages_de.json: " + err.Error())
	}
	return m
}()

// Language returns the language of messages that suits an Accept-Language header best: "de" or "en".
func Language(header string) string {
	best, bestQ := "en", -1.0
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		lang, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if (lang == "de" || lang == "en") && q > bestQ {
			best, bestQ = lang, q
		}
	}
	return best
}

// Message is a text with values that is not an error itself, such as one of the reasons an error gives: it is
// worded in English and translated like the messages of errors (Msg makes one, so that the catalog's test finds it).
type Message struct {
	Format string
	Args   []any
}

// Msg returns a message.
func Msg(format string, args ...any) Message { return Message{Format: format, Args: args} }

// String returns the message in English.
func (m Message) String() string {
	if len(m.Args) == 0 {
		return m.Format
	}
	return fmt.Sprintf(m.Format, m.Args...)
}

// In returns the message worded in a language; without a translation it stays English.
func (m Message) In(lang string) string {
	t, ok := catalogDE[m.Format]
	if lang != "de" || !ok {
		return m.String()
	}
	if len(m.Args) == 0 {
		return t
	}
	return fmt.Sprintf(t, m.Args...)
}

// Messages stand one after another, as sentences: the value of an error that lists reasons. Their English is
// their String; Localize translates each of them.
type Messages []Message

// String returns the messages in English.
func (l Messages) String() string { return l.In("en") }

// In returns the messages worded in a language.
func (l Messages) In(lang string) string {
	parts := make([]string, len(l))
	for i, m := range l {
		parts[i] = m.In(lang)
	}
	return strings.Join(parts, " ")
}

// Localize returns the error worded in a language; without a translation it stays English. Values and field
// messages that are messages of their own (such as the hints of invalid fields) are translated, too.
func (e *Error) Localize(lang string) *Error {
	if e == nil || lang != "de" {
		return e
	}
	c := *e
	if t, ok := catalogDE[e.template]; ok && e.template != "" {
		c.Detail = t
		if len(e.args) > 0 {
			args := make([]any, len(e.args))
			for i, a := range e.args {
				args[i] = a
				switch v := a.(type) {
				case string:
					if ts, ok := catalogDE[v]; ok {
						args[i] = ts
					}
				case Message:
					args[i] = v.In(lang)
				case Messages:
					args[i] = v.In(lang)
				}
			}
			c.Detail = fmt.Sprintf(t, args...)
		}
	}
	if len(e.Errors) > 0 {
		c.Errors = make([]FieldError, len(e.Errors))
		for i, f := range e.Errors {
			if t, ok := catalogDE[f.Message]; ok {
				f.Message = t
			} else if f.Message == e.Detail {
				f.Message = c.Detail // a field message that repeats the error's
			}
			c.Errors[i] = f
		}
	}
	return &c
}

// Text words a message of the server that is no error — such as the note of a result — in a language, like the
// messages of errors: an English format with its values, and its German wording in messages_de.json.
func Text(lang, format string, args ...any) string {
	return New(0, "", format, args...).Localize(lang).Detail
}
