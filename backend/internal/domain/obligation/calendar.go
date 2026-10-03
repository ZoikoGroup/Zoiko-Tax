package obligation

import (
	"fmt"
	"sort"
	"time"
)

// PeriodKind is the closed set of period shapes (ZTAX-OBL-REQ-0078).
type PeriodKind string

// The period kinds.
const (
	PeriodTransaction PeriodKind = "TRANSACTION"
	PeriodMonth       PeriodKind = "MONTH"
	PeriodQuarter     PeriodKind = "QUARTER"
	PeriodYear        PeriodKind = "YEAR"
	PeriodRolling     PeriodKind = "ROLLING"
	PeriodEvent       PeriodKind = "EVENT"
	PeriodCustom      PeriodKind = "CUSTOM"
)

// Span is a half-open interval [Start, End). A TRANSACTION or EVENT span has
// Start == End: it is an instant, not a period.
type Span struct {
	Start time.Time
	End   time.Time
}

// Contains reports whether t falls in the span.
func (s Span) Contains(t time.Time) bool {
	if s.Start.Equal(s.End) {
		return t.Equal(s.Start)
	}
	return !t.Before(s.Start) && t.Before(s.End)
}

// PeriodRule is content: how an event time maps to a legal period.
type PeriodRule struct {
	Kind PeriodKind
	// Timezone is the legal calendar's IANA zone, explicit and required
	// (ZTAX-OBL-REQ-0079). "Monthly" means a month in somebody's calendar.
	Timezone string
	// YearStartMonth is the first month of a YEAR or the first quarter's
	// first month; 1 when the legal year is the calendar year.
	YearStartMonth time.Month
	// RollingMonths is a ROLLING window's length, ending at the event, with
	// RollingIncludesEvent saying whether the event's own instant is inside
	// (ZTAX-OBL-REQ-0067: the exact window and its inclusion rule).
	RollingMonths        int
	RollingIncludesEvent bool
	// Boundaries are a CUSTOM rule's period starts, ascending; each period
	// runs to the next boundary.
	Boundaries []time.Time
}

// Validate refuses a rule that cannot place an event.
func (r PeriodRule) Validate() error {
	if r.Timezone == "" {
		return fmt.Errorf("period rule names no legal timezone")
	}
	switch r.Kind {
	case PeriodTransaction, PeriodEvent, PeriodMonth:
	case PeriodQuarter, PeriodYear:
		if r.YearStartMonth < time.January || r.YearStartMonth > time.December {
			return fmt.Errorf("%s period rule has year start month %d", r.Kind, r.YearStartMonth)
		}
	case PeriodRolling:
		if r.RollingMonths < 1 {
			return fmt.Errorf("rolling period rule has a window of %d months", r.RollingMonths)
		}
	case PeriodCustom:
		if len(r.Boundaries) < 2 {
			return fmt.Errorf("custom period rule needs at least two boundaries")
		}
		for i := 1; i < len(r.Boundaries); i++ {
			if !r.Boundaries[i].After(r.Boundaries[i-1]) {
				return fmt.Errorf("custom period boundaries are not strictly ascending at %d", i)
			}
		}
	default:
		return fmt.Errorf("period kind %q is not in the closed set", r.Kind)
	}
	return nil
}

// checkLocation refuses a location that is not the rule's declared zone. The
// caller loads the zone — reading the tz database is I/O, which the domain
// does not do — and this is what stops it loading the wrong one.
func checkLocation(declared string, loc *time.Location) error {
	if loc == nil || loc.String() != declared {
		got := "nil"
		if loc != nil {
			got = loc.String()
		}
		return fmt.Errorf("the legal calendar is %s; the location supplied is %s", declared, got)
	}
	return nil
}

// PeriodFor places an event in its legal period.
//
// It takes the event time and nothing else about the event. In particular it
// takes no receipt or processing time, so a late-arriving event is placed in
// the period its event time falls in (ZTAX-OBL-REQ-0062), and when it arrived
// has no route into the answer (ZTAX-OBL-REQ-0063).
func (r PeriodRule) PeriodFor(eventTime time.Time, loc *time.Location) (Span, error) {
	if err := r.Validate(); err != nil {
		return Span{}, err
	}
	if err := checkLocation(r.Timezone, loc); err != nil {
		return Span{}, err
	}
	t := eventTime.In(loc)
	switch r.Kind {
	case PeriodTransaction, PeriodEvent:
		return Span{Start: eventTime, End: eventTime}, nil
	case PeriodMonth:
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		return Span{Start: start, End: start.AddDate(0, 1, 0)}, nil
	case PeriodQuarter, PeriodYear:
		months := 3
		if r.Kind == PeriodYear {
			months = 12
		}
		// Months since the legal year began, floored to the period length.
		offset := (int(t.Month()) - int(r.YearStartMonth) + 12) % 12
		startMonth := int(t.Month()) - offset%months
		start := time.Date(t.Year(), time.Month(startMonth), 1, 0, 0, 0, 0, loc)
		return Span{Start: start, End: start.AddDate(0, months, 0)}, nil
	case PeriodRolling:
		end := t
		if r.RollingIncludesEvent {
			end = t.Add(time.Nanosecond)
		}
		return Span{Start: end.AddDate(0, -r.RollingMonths, 0), End: end}, nil
	case PeriodCustom:
		i := sort.Search(len(r.Boundaries), func(i int) bool { return r.Boundaries[i].After(eventTime) })
		if i == 0 || i == len(r.Boundaries) {
			return Span{}, fmt.Errorf("event at %s falls outside the custom periods", eventTime.Format(time.RFC3339))
		}
		return Span{Start: r.Boundaries[i-1], End: r.Boundaries[i]}, nil
	}
	return Span{}, fmt.Errorf("period kind %q", r.Kind)
}

// BusinessDayAdjustment is how a due date that falls on a non-business day
// moves. Content-driven and declared; NONE is a declaration, not a default
// (ZTAX-OBL-REQ-0080).
type BusinessDayAdjustment string

// The adjustments.
const (
	AdjustNone              BusinessDayAdjustment = "NONE"
	AdjustFollowing         BusinessDayAdjustment = "FOLLOWING"
	AdjustPreceding         BusinessDayAdjustment = "PRECEDING"
	AdjustModifiedFollowing BusinessDayAdjustment = "MODIFIED_FOLLOWING"
)

// CalendarRef pins a holiday calendar version (ZTAX-OBL-REQ-0081).
type CalendarRef struct {
	ID      string
	Version string
}

// HolidayCalendar is one version of a jurisdiction's business-day calendar.
type HolidayCalendar struct {
	Ref      CalendarRef
	Weekend  []time.Weekday
	Holidays map[string]bool // civil dates, "2027-01-01"
}

// BusinessDay reports whether a civil date is a business day.
func (c HolidayCalendar) BusinessDay(d time.Time) bool {
	for _, w := range c.Weekend {
		if d.Weekday() == w {
			return false
		}
	}
	return !c.Holidays[d.Format(time.DateOnly)]
}

// DueAnchor is what a due date counts from.
type DueAnchor string

// The anchors.
const (
	AnchorPeriodEnd DueAnchor = "PERIOD_END"
	AnchorEvent     DueAnchor = "EVENT"
)

// DueDateRule is content: when a period's duty falls due.
type DueDateRule struct {
	Anchor DueAnchor
	// OffsetMonths then OffsetDays move from the anchor's civil date. A
	// PERIOD_END anchor is the last civil day of the period.
	OffsetMonths int
	OffsetDays   int
	// DayOfMonth, when set, replaces the day after the month offset.
	DayOfMonth int
	Adjustment BusinessDayAdjustment
	// Calendar is required whenever Adjustment is not NONE.
	Calendar *CalendarRef
	// ExtensionDays is the revision window after the legal due date. It is
	// reported beside the legal date, never in place of it
	// (ZTAX-OBL-REQ-0082).
	ExtensionDays int
	// InternalCutoffDays is the operator's own cutoff before the legal due
	// date. Also reported beside it (ZTAX-OBL-REQ-0083).
	InternalCutoffDays int
}

// Validate refuses a rule that cannot produce a due date.
func (r DueDateRule) Validate() error {
	switch r.Anchor {
	case AnchorPeriodEnd, AnchorEvent:
	default:
		return fmt.Errorf("due-date rule has anchor %q", r.Anchor)
	}
	switch r.Adjustment {
	case AdjustNone:
	case AdjustFollowing, AdjustPreceding, AdjustModifiedFollowing:
		if r.Calendar == nil || r.Calendar.ID == "" || r.Calendar.Version == "" {
			return fmt.Errorf("due-date rule adjusts for business days and pins no holiday calendar version")
		}
	default:
		return fmt.Errorf("due-date rule declares no business-day adjustment")
	}
	if r.DayOfMonth < 0 || r.DayOfMonth > 31 {
		return fmt.Errorf("due-date rule has day of month %d", r.DayOfMonth)
	}
	if r.ExtensionDays < 0 || r.InternalCutoffDays < 0 {
		return fmt.Errorf("due-date rule has a negative extension or cutoff")
	}
	return nil
}

// DueDates are the three dates a duty carries, kept apart. The legal due date
// is the statute's; the extended date is the revision window; the internal
// cutoff is ours. None overwrites another.
type DueDates struct {
	Legal          time.Time
	Extended       *time.Time
	InternalCutoff *time.Time
	// Calendar is the holiday calendar version the legal date depended on,
	// if any.
	Calendar *CalendarRef
}

// Compute produces the due dates for a period (or, for an EVENT anchor, an
// event). cal must be the version the rule pins.
func (r DueDateRule) Compute(span Span, loc *time.Location, cal *HolidayCalendar) (DueDates, error) {
	if err := r.Validate(); err != nil {
		return DueDates{}, err
	}
	if loc == nil {
		return DueDates{}, fmt.Errorf("due dates need the legal calendar's location")
	}
	if r.Adjustment != AdjustNone {
		if cal == nil || cal.Ref != *r.Calendar {
			return DueDates{}, fmt.Errorf("the rule pins holiday calendar %s@%s and it was not supplied", r.Calendar.ID, r.Calendar.Version)
		}
	}
	var anchor time.Time
	switch r.Anchor {
	case AnchorPeriodEnd:
		last := span.End.In(loc)
		if !span.Start.Equal(span.End) {
			// The period ends at midnight starting the next period; its last
			// civil day is the day before.
			last = last.AddDate(0, 0, -1)
		}
		anchor = civil(last, loc)
	case AnchorEvent:
		anchor = civil(span.Start.In(loc), loc)
	}
	due := anchor
	if r.OffsetMonths != 0 {
		// Move to the first of the month before offsetting, so 31 January
		// plus one month is February rather than 3 March.
		first := time.Date(due.Year(), due.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, r.OffsetMonths, 0)
		day := due.Day()
		if r.DayOfMonth > 0 {
			day = r.DayOfMonth
		}
		last := first.AddDate(0, 1, -1).Day()
		if day > last {
			day = last
		}
		due = time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, loc)
	} else if r.DayOfMonth > 0 {
		due = time.Date(due.Year(), due.Month(), r.DayOfMonth, 0, 0, 0, 0, loc)
	}
	due = due.AddDate(0, 0, r.OffsetDays)
	due = r.adjust(due, cal)

	out := DueDates{Legal: due}
	if r.Adjustment != AdjustNone {
		ref := *r.Calendar
		out.Calendar = &ref
	}
	if r.ExtensionDays > 0 {
		e := r.adjust(due.AddDate(0, 0, r.ExtensionDays), cal)
		out.Extended = &e
	}
	if r.InternalCutoffDays > 0 {
		c := due.AddDate(0, 0, -r.InternalCutoffDays)
		out.InternalCutoff = &c
	}
	return out, nil
}

func (r DueDateRule) adjust(d time.Time, cal *HolidayCalendar) time.Time {
	if r.Adjustment == AdjustNone || cal == nil {
		return d
	}
	step := 1
	if r.Adjustment == AdjustPreceding {
		step = -1
	}
	out := d
	for !cal.BusinessDay(out) {
		out = out.AddDate(0, 0, step)
	}
	if r.Adjustment == AdjustModifiedFollowing && out.Month() != d.Month() {
		out = d
		for !cal.BusinessDay(out) {
			out = out.AddDate(0, 0, -1)
		}
	}
	return out
}

func civil(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
