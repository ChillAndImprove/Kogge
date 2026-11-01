// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"slices"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/apperr"
)

// TestScheduleNext: when subscriptions send next, in their time zone (7 October 2026 is a Wednesday).
func TestScheduleNext(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no time zone database")
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	workdays, everyDay := []int{1, 2, 3, 4, 5}, []int{1, 2, 3, 4, 5, 6, 7}
	daily := Schedule{Frequency: "daily", Minute: 8*60 + 30, Weekdays: workdays}
	office := Schedule{Frequency: "hourly", Minute: 0, Weekdays: workdays, Hours: []int{8, 9, 10, 11, 12, 13, 14, 15, 16, 17}}
	monthly := Schedule{Frequency: "monthly", Minute: 7 * 60, MonthDay: 15}
	last := Schedule{Frequency: "monthly", Minute: 7 * 60, MonthDay: 31}
	for _, c := range []struct {
		name        string
		sc          Schedule
		after, want string
	}{
		{"daily, later today", daily, "2026-10-07 07:00", "2026-10-07 08:30"},
		{"daily, at the time", daily, "2026-10-07 08:30", "2026-10-08 08:30"},
		{"daily, over the weekend", daily, "2026-10-09 09:00", "2026-10-12 08:30"},
		{"daily, on Sundays", Schedule{Frequency: "daily", Minute: 18 * 60, Weekdays: []int{7}}, "2026-10-07 09:00", "2026-10-11 18:00"},
		{"hourly", Schedule{Frequency: "hourly", Minute: 15, Weekdays: everyDay}, "2026-10-07 10:20", "2026-10-07 11:15"},
		{"hourly, in the hour", Schedule{Frequency: "hourly", Minute: 45, Weekdays: everyDay}, "2026-10-07 10:20", "2026-10-07 10:45"},
		{"hourly, office hours", office, "2026-10-07 12:10", "2026-10-07 13:00"},
		{"hourly, after office hours", office, "2026-10-07 17:30", "2026-10-08 08:00"},
		{"hourly, Friday evening", office, "2026-10-09 18:00", "2026-10-12 08:00"},
		{"hourly, through the night", Schedule{Frequency: "hourly", Weekdays: everyDay, Hours: []int{22, 2}}, "2026-10-07 23:30",
			"2026-10-08 02:00"},
		{"monthly", monthly, "2026-10-07 09:00", "2026-10-15 07:00"},
		{"monthly, at the time", monthly, "2026-10-15 07:00", "2026-11-15 07:00"},
		{"monthly, the 31st of a month with 30 days", last, "2026-11-01 00:00", "2026-11-30 07:00"},
		{"monthly, the 31st in February", last, "2027-02-01 00:00", "2027-02-28 07:00"},
		{"monthly, over the year", monthly, "2026-12-20 00:00", "2027-01-15 07:00"},
		// The hour from 02:00 to 03:00 does not exist on 28 March 2027 in Berlin.
		{"daily, in the hour daylight saving time skips", Schedule{Frequency: "daily", Minute: 2*60 + 30, Weekdays: everyDay},
			"2027-03-28 00:00", "2027-03-28 03:30"},
	} {
		if got := c.sc.Next(at(c.after), berlin); !got.Equal(at(c.want)) {
			t.Errorf("%s: %s, want %s", c.name, got.In(berlin).Format("Mon 2006-01-02 15:04"), c.want)
		}
	}
	// The time zone decides, not the zone of the time given.
	if got := daily.Next(at("2026-10-07 07:00").UTC(), berlin); !got.Equal(at("2026-10-07 08:30")) {
		t.Errorf("from UTC: %s", got)
	}
}

// TestScheduleNormalized: schedules are checked and keep only what their frequency uses.
func TestScheduleNormalized(t *testing.T) {
	got, err := Schedule{Frequency: "hourly", Minute: 5, Weekdays: []int{5, 1, 5}, Hours: []int{17, 8}, MonthDay: 3}.normalized()
	if err != nil || !slices.Equal(got.Weekdays, []int{1, 5}) || !slices.Equal(got.Hours, []int{8, 17}) || got.MonthDay != 0 {
		t.Errorf("hourly: %+v %v", got, err)
	}
	got, err = Schedule{Frequency: "daily", Minute: 600, Weekdays: []int{2}, Hours: []int{3}}.normalized()
	if err != nil || len(got.Hours) != 0 || !slices.Equal(got.Weekdays, []int{2}) {
		t.Errorf("daily: %+v %v", got, err)
	}
	got, err = Schedule{Frequency: "monthly", Minute: 600, Weekdays: []int{2}, MonthDay: 31}.normalized()
	if err != nil || len(got.Weekdays) != 0 || got.MonthDay != 31 {
		t.Errorf("monthly: %+v %v", got, err)
	}
	for _, c := range []struct {
		code string
		sc   Schedule
	}{
		{"subscription.minute", Schedule{Frequency: "hourly", Minute: 60, Weekdays: []int{1}}},
		{"subscription.hours", Schedule{Frequency: "hourly", Weekdays: []int{1}, Hours: []int{24}}},
		{"subscription.weekdays", Schedule{Frequency: "daily", Minute: 60}},
		{"subscription.weekdays", Schedule{Frequency: "hourly", Weekdays: []int{0, 1}}},
		{"subscription.weekdays", Schedule{Frequency: "daily", Weekdays: []int{8}}},
		{"subscription.month-day", Schedule{Frequency: "monthly"}},
		{"subscription.frequency", Schedule{Frequency: "weekly", Weekdays: []int{1}}},
	} {
		if _, err := c.sc.normalized(); !apperr.Is(err, c.code) {
			t.Errorf("%+v: %v, want %s", c.sc, err, c.code)
		}
	}
}
