// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package db

import (
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Args collects bound parameters for dynamically built SQL: Add returns the placeholder ($n) for a value.
type Args []any

// Add appends a value and returns its placeholder.
func (a *Args) Add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// CustomPlan returns the arguments of a query that PostgreSQL is to plan for its values every time. After a few
// runs of a prepared statement it may plan it once for all values; where the best plan depends on the values — how
// many titles a word begins, how much of the data the principal may see — that plan may cost a hundredfold (found
// by the scale tests of the quick search). pgx runs the query as an unnamed statement then, which is planned with
// its values.
func CustomPlan(args Args) []any {
	return append([]any{pgx.QueryExecModeCacheDescribe}, args...)
}
