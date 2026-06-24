package scheduler

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"goxidized/pkg/goxidized"
)

type JitterFunc func(key string, max time.Duration) time.Duration

type SchedulePolicy struct {
	Interval      time.Duration
	Cron          string
	Location      *time.Location
	JitterPercent int
	JitterMax     time.Duration
	Jitter        JitterFunc
	Windows       []ScheduleWindow
	Blackouts     []ScheduleWindow
}

type ScheduleWindow struct {
	Name     string
	Days     []time.Weekday
	Start    time.Duration
	End      time.Duration
	Location *time.Location
	Groups   []string
	Sites    []string
	Vendors  []string
	Roles    []string
}

type ScheduleDecision struct {
	BaseTime          time.Time
	JitteredTime      time.Time
	QueueAt           time.Time
	Jitter            time.Duration
	DelayedByWindow   bool
	DelayedByBlackout bool
}

type Planner struct {
	policy SchedulePolicy
	cron   *cronSpec
	loc    *time.Location
	jitter JitterFunc
}

func NewPlanner(policy SchedulePolicy) (*Planner, error) {
	loc := policy.Location
	if loc == nil {
		loc = time.UTC
	}
	p := &Planner{policy: policy, loc: loc, jitter: policy.Jitter}
	if p.jitter == nil {
		p.jitter = DeterministicJitter
	}
	if strings.TrimSpace(policy.Cron) != "" {
		spec, err := parseCron(policy.Cron)
		if err != nil {
			return nil, err
		}
		p.cron = spec
	}
	for _, window := range append(append([]ScheduleWindow{}, policy.Windows...), policy.Blackouts...) {
		if window.Start < 0 || window.Start >= 24*time.Hour || window.End < 0 || window.End > 24*time.Hour {
			return nil, fmt.Errorf("schedule window %q has invalid start/end", window.Name)
		}
	}
	return p, nil
}

func (p *Planner) InitialBase(now time.Time) (time.Time, error) {
	if p.cron != nil {
		return p.cron.next(now.In(p.loc), p.loc)
	}
	if p.policy.Interval <= 0 {
		return time.Time{}, errors.New("scheduler interval or cron is required")
	}
	return now.UTC(), nil
}

func (p *Planner) NextBase(after time.Time) (time.Time, error) {
	if p.cron != nil {
		return p.cron.next(after.In(p.loc), p.loc)
	}
	if p.policy.Interval <= 0 {
		return time.Time{}, errors.New("scheduler interval or cron is required")
	}
	return after.UTC().Add(p.policy.Interval), nil
}

func (p *Planner) QueueTime(base time.Time, target goxidized.Target) (ScheduleDecision, error) {
	maxJitter := p.maxJitter()
	var jitter time.Duration
	if maxJitter > 0 {
		jitter = p.jitter(target.ID, maxJitter)
		if jitter < 0 {
			jitter = 0
		}
		if jitter > maxJitter {
			jitter = maxJitter
		}
	}
	jittered := base.UTC().Add(jitter)
	queueAt, delayedWindow, delayedBlackout, err := p.nextAllowed(jittered, target)
	if err != nil {
		return ScheduleDecision{}, err
	}
	return ScheduleDecision{
		BaseTime: base.UTC(), JitteredTime: jittered, QueueAt: queueAt.UTC(), Jitter: jitter,
		DelayedByWindow: delayedWindow, DelayedByBlackout: delayedBlackout,
	}, nil
}

func (p *Planner) maxJitter() time.Duration {
	if p.policy.JitterPercent <= 0 {
		return 0
	}
	if p.policy.JitterMax > 0 {
		return p.policy.JitterMax
	}
	if p.policy.Interval > 0 {
		return p.policy.Interval * time.Duration(p.policy.JitterPercent) / 100
	}
	return 0
}

func (p *Planner) nextAllowed(candidate time.Time, target goxidized.Target) (time.Time, bool, bool, error) {
	windows := applicableWindows(p.policy.Windows, target)
	blackouts := applicableWindows(p.policy.Blackouts, target)
	ok, windowBlocked, blackoutBlocked := allowedAt(candidate, windows, blackouts)
	if ok {
		return candidate.UTC(), false, false, nil
	}
	delayedWindow := windowBlocked
	delayedBlackout := blackoutBlocked
	next := candidate.Truncate(time.Minute)
	if !next.After(candidate) {
		next = next.Add(time.Minute)
	}
	deadline := candidate.AddDate(1, 0, 0)
	for !next.After(deadline) {
		ok, windowBlocked, blackoutBlocked = allowedAt(next, windows, blackouts)
		delayedWindow = delayedWindow || windowBlocked
		delayedBlackout = delayedBlackout || blackoutBlocked
		if ok {
			return next.UTC(), delayedWindow, delayedBlackout, nil
		}
		next = next.Add(time.Minute)
	}
	return time.Time{}, delayedWindow, delayedBlackout, errors.New("no allowed schedule window found within one year")
}

func applicableWindows(windows []ScheduleWindow, target goxidized.Target) []ScheduleWindow {
	out := make([]ScheduleWindow, 0, len(windows))
	for _, window := range windows {
		if window.appliesTo(target) {
			out = append(out, window)
		}
	}
	return out
}

func allowedAt(t time.Time, windows, blackouts []ScheduleWindow) (bool, bool, bool) {
	if len(windows) > 0 {
		inWindow := false
		for _, window := range windows {
			if window.Contains(t) {
				inWindow = true
				break
			}
		}
		if !inWindow {
			return false, true, false
		}
	}
	for _, blackout := range blackouts {
		if blackout.Contains(t) {
			return false, false, true
		}
	}
	return true, false, false
}

func (w ScheduleWindow) Contains(t time.Time) bool {
	loc := w.Location
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	tod := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second + time.Duration(local.Nanosecond())
	if w.Start == w.End {
		return w.dayAllowed(local.Weekday())
	}
	if w.Start < w.End {
		return w.dayAllowed(local.Weekday()) && tod >= w.Start && tod < w.End
	}
	if tod >= w.Start {
		return w.dayAllowed(local.Weekday())
	}
	if tod < w.End {
		return w.dayAllowed(previousWeekday(local.Weekday()))
	}
	return false
}

func (w ScheduleWindow) appliesTo(t goxidized.Target) bool {
	return matchesSelector(w.Groups, t.Group) &&
		matchesSelector(w.Sites, t.Site) &&
		matchesSelector(w.Vendors, t.Vendor) &&
		matchesSelector(w.Roles, t.Role)
}

func (w ScheduleWindow) dayAllowed(day time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, allowed := range w.Days {
		if allowed == day {
			return true
		}
	}
	return false
}

func matchesSelector(values []string, got string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == got {
			return true
		}
	}
	return false
}

func previousWeekday(day time.Weekday) time.Weekday {
	if day == time.Sunday {
		return time.Saturday
	}
	return day - 1
}

func DeterministicJitter(key string, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum64() % uint64(max))
}

func ParseClock(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, errors.New("clock value is required")
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid clock value %q", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour in %q", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute in %q", value)
	}
	second := 0
	if len(parts) == 3 {
		second, err = strconv.Atoi(parts[2])
		if err != nil {
			return 0, fmt.Errorf("invalid second in %q", value)
		}
	}
	if hour < 0 || hour > 24 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return 0, fmt.Errorf("invalid clock value %q", value)
	}
	if hour == 24 && (minute != 0 || second != 0) {
		return 0, fmt.Errorf("invalid clock value %q", value)
	}
	return time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second, nil
}

func ParseWeekdays(values []string) ([]time.Weekday, error) {
	var out []time.Weekday
	for _, raw := range values {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "", "*", "all", "daily", "everyday":
			return nil, nil
		case "weekday", "weekdays":
			out = append(out, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday)
		case "weekend", "weekends":
			out = append(out, time.Saturday, time.Sunday)
		default:
			day, ok := weekdayAliases[strings.ToLower(strings.TrimSpace(raw))]
			if !ok {
				return nil, fmt.Errorf("invalid weekday %q", raw)
			}
			out = append(out, day)
		}
	}
	return dedupeWeekdays(out), nil
}

func dedupeWeekdays(in []time.Weekday) []time.Weekday {
	seen := map[time.Weekday]bool{}
	out := make([]time.Weekday, 0, len(in))
	for _, day := range in {
		if !seen[day] {
			seen[day] = true
			out = append(out, day)
		}
	}
	return out
}

var weekdayAliases = map[string]time.Weekday{
	"0": time.Sunday, "7": time.Sunday, "sun": time.Sunday, "sunday": time.Sunday,
	"1": time.Monday, "mon": time.Monday, "monday": time.Monday,
	"2": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"3": time.Wednesday, "wed": time.Wednesday, "wednesday": time.Wednesday,
	"4": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"5": time.Friday, "fri": time.Friday, "friday": time.Friday,
	"6": time.Saturday, "sat": time.Saturday, "saturday": time.Saturday,
}

type cronSpec struct {
	minute cronField
	hour   cronField
	dom    cronField
	month  cronField
	dow    cronField
}

type cronField struct {
	values   map[int]bool
	wildcard bool
}

func parseCron(expr string) (*cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q must have five fields", expr)
	}
	minute, err := parseCronField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("cron minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("cron hour: %w", err)
	}
	dom, err := parseCronField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-month: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12, monthAliases)
	if err != nil {
		return nil, fmt.Errorf("cron month: %w", err)
	}
	dow, err := parseCronField(fields[4], 0, 7, weekdayNumberAliases)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-week: %w", err)
	}
	return &cronSpec{minute: minute, hour: hour, dom: dom, month: month, dow: dow}, nil
}

func parseCronField(raw string, min, max int, aliases map[string]int) (cronField, error) {
	field := cronField{values: map[int]bool{}, wildcard: raw == "*"}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return field, errors.New("empty cron field part")
		}
		step := 1
		if stepParts := strings.Split(part, "/"); len(stepParts) == 2 {
			part = stepParts[0]
			parsed, err := strconv.Atoi(stepParts[1])
			if err != nil || parsed <= 0 {
				return field, fmt.Errorf("invalid step %q", stepParts[1])
			}
			step = parsed
		} else if strings.Count(part, "/") > 0 {
			return field, fmt.Errorf("invalid step expression %q", part)
		}
		start, end, err := cronRange(part, min, max, aliases)
		if err != nil {
			return field, err
		}
		for value := start; value <= end; value += step {
			normalized := value
			if max == 7 && value == 7 {
				normalized = 0
			}
			field.values[normalized] = true
		}
	}
	return field, nil
}

func cronRange(raw string, min, max int, aliases map[string]int) (int, int, error) {
	if raw == "*" || raw == "" {
		return min, max, nil
	}
	if strings.Contains(raw, "-") {
		parts := strings.Split(raw, "-")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid range %q", raw)
		}
		start, err := cronValue(parts[0], aliases)
		if err != nil {
			return 0, 0, err
		}
		end, err := cronValue(parts[1], aliases)
		if err != nil {
			return 0, 0, err
		}
		if start > end {
			return 0, 0, fmt.Errorf("invalid descending range %q", raw)
		}
		if start < min || end > max {
			return 0, 0, fmt.Errorf("range %q outside %d-%d", raw, min, max)
		}
		return start, end, nil
	}
	value, err := cronValue(raw, aliases)
	if err != nil {
		return 0, 0, err
	}
	if value < min || value > max {
		return 0, 0, fmt.Errorf("value %d outside %d-%d", value, min, max)
	}
	return value, value, nil
}

func cronValue(raw string, aliases map[string]int) (int, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if aliases != nil {
		if value, ok := aliases[key]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(key)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", raw)
	}
	return value, nil
}

func (c *cronSpec) next(after time.Time, loc *time.Location) (time.Time, error) {
	next := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := next.AddDate(5, 0, 0)
	for !next.After(deadline) {
		if c.matches(next) {
			return next.UTC(), nil
		}
		next = next.Add(time.Minute)
	}
	return time.Time{}, errors.New("no cron match found within five years")
}

func (c *cronSpec) matches(t time.Time) bool {
	if !c.minute.matches(t.Minute()) || !c.hour.matches(t.Hour()) || !c.month.matches(int(t.Month())) {
		return false
	}
	dom := c.dom.matches(t.Day())
	dow := c.dow.matches(int(t.Weekday()))
	if !c.dom.wildcard && !c.dow.wildcard {
		return dom || dow
	}
	return dom && dow
}

func (f cronField) matches(value int) bool {
	return f.values[value]
}

var monthAliases = map[string]int{
	"jan": 1, "january": 1,
	"feb": 2, "february": 2,
	"mar": 3, "march": 3,
	"apr": 4, "april": 4,
	"may": 5,
	"jun": 6, "june": 6,
	"jul": 7, "july": 7,
	"aug": 8, "august": 8,
	"sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10,
	"nov": 11, "november": 11,
	"dec": 12, "december": 12,
}

var weekdayNumberAliases = map[string]int{
	"sun": 0, "sunday": 0,
	"mon": 1, "monday": 1,
	"tue": 2, "tues": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3,
	"thu": 4, "thur": 4, "thurs": 4, "thursday": 4,
	"fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
}
