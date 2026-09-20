package domain

import "time"

// Jakarta is the campaign's reference timezone. "Each user can earn at most
// 50,000 IDR of cashback per day" is inherently ambiguous without a timezone:
// a UTC day boundary would shift the reset by 7 hours for an Indonesian
// audience. We define "per day" as a calendar day in Asia/Jakarta (WIB, UTC+7).
//
// The tzdata database is embedded into the binary via `import _ "time/tzdata"`
// in main, so this resolves even on a minimal container image without the OS
// tzdata package.
var Jakarta = mustLoadJakarta()

func mustLoadJakarta() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic("domain: cannot load Asia/Jakarta timezone: " + err.Error())
	}
	return loc
}

// JakartaDay returns the calendar date (at midnight WIB) that the given instant
// falls on in Jakarta. This is the bucket key for the per-user daily cap.
func JakartaDay(t time.Time) time.Time {
	y, m, d := t.In(Jakarta).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, Jakarta)
}
