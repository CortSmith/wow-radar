package logparse

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bounds for the timestamp fields.
const (
	maxOffsetHours   = 14
	maxOffsetMinutes = 59
	secondsPerHour   = 3600
	secondsPerMinute = 60
	nanoDigits       = 9
	monthsPerYear    = 12
)

// parseTimestamp parses "M/D/YYYY HH:MM:SS[.mmm][+-H[:MM]]" into UTC. The line's own offset wins;
// otherwise loc applies (nil means UTC).
func parseTimestamp(s string, loc *time.Location) (time.Time, error) {
	bad := func() (time.Time, error) { return time.Time{}, fmt.Errorf("%w: timestamp %q", ErrBadLine, s) }
	datePart, clock, ok := strings.Cut(s, " ")
	if !ok {
		return bad()
	}
	clock, offset, hasOffset, ok := splitOffset(clock)
	if !ok {
		return bad()
	}
	nums, ns, ok := timeNumbers(datePart, clock)
	if !ok {
		return bad()
	}
	month, day, year, hh, mm, ss := nums[0], nums[1], nums[2], nums[3], nums[4], nums[5]
	if month < 1 || month > monthsPerYear || day < 1 || hh < 0 || hh > 23 || mm < 0 || mm > 59 || ss < 0 || ss > 59 {
		return bad()
	}
	zone := time.UTC
	if !hasOffset && loc != nil {
		zone = loc
	}
	t := time.Date(year, time.Month(month), day, hh, mm, ss, ns, zone)
	if t.Day() != day { // day beyond the month's length rolled over
		return bad()
	}
	if hasOffset {
		t = t.Add(-offset)
	}
	return t.UTC(), nil
}

// splitOffset separates a trailing "+-H[:MM]" offset from the clock text.
func splitOffset(clock string) (rest string, offset time.Duration, has, ok bool) {
	k := strings.LastIndexAny(clock, "+-")
	if k <= 0 {
		return clock, 0, false, true
	}
	text := clock[k:]
	sign := time.Duration(1)
	if text[0] == '-' {
		sign = -1
	}
	hoursText, minutesText, _ := strings.Cut(text[1:], ":")
	hours, err := strconv.Atoi(hoursText)
	if err != nil || hours < 0 || hours > maxOffsetHours {
		return "", 0, false, false
	}
	minutes := 0
	if minutesText != "" {
		if minutes, err = strconv.Atoi(minutesText); err != nil || minutes < 0 || minutes > maxOffsetMinutes {
			return "", 0, false, false
		}
	}
	seconds := hours*secondsPerHour + minutes*secondsPerMinute
	return clock[:k], sign * time.Duration(seconds) * time.Second, true, true
}

// timeNumbers parses month, day, year, hour, minute, second and the nanoseconds of the fraction.
func timeNumbers(datePart, clock string) (nums [6]int, ns int, ok bool) {
	secs, frac, _ := strings.Cut(clock, ".")
	fields := append(strings.Split(datePart, "/"), strings.Split(secs, ":")...)
	if len(fields) != len(nums) {
		return nums, 0, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nums, 0, false
		}
		nums[i] = n
	}
	if frac == "" {
		return nums, 0, true
	}
	frac = frac[:min(len(frac), nanoDigits)]
	n, err := strconv.Atoi(frac)
	if err != nil || n < 0 {
		return nums, 0, false
	}
	for k := len(frac); k < nanoDigits; k++ {
		n *= 10
	}
	return nums, n, true
}
