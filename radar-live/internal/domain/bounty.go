package domain

import (
	"cmp"
	"slices"
)

// Points awarded for bounty actions; assists are tracked but worth nothing.
const (
	BountyPointsPerKillingBlow = 10
	BountyPointsPerAssist      = 0
	// BountyDefaultLimit and BountyMaxLimit page the ranking.
	BountyDefaultLimit = 50
	BountyMaxLimit     = 500
)

// BountyRow is one line of the bounty ranking.
type BountyRow struct {
	Rank    int    `json:"rank"`
	GUID    string `json:"guid"`
	Name    string `json:"name"`
	KB      int64  `json:"kb"`
	Assists int64  `json:"assists"`
	Score   int64  `json:"score"`
}

// RankBounty ranks every contact with a killing blow or an assist. Order: killing blows, assists,
// most recent activity, then guid, which is a total order so ranks are stable.
func RankBounty(contacts []Contact) []BountyRow {
	scored := make([]Contact, 0, len(contacts))
	for _, c := range contacts {
		if c.KB > 0 || c.Assists > 0 {
			scored = append(scored, c)
		}
	}
	slices.SortFunc(scored, func(a, b Contact) int {
		return cmp.Or(
			cmp.Compare(b.KB, a.KB),
			cmp.Compare(b.Assists, a.Assists),
			cmp.Compare(b.LastSeen, a.LastSeen),
			cmp.Compare(a.GUID, b.GUID),
		)
	})
	rows := make([]BountyRow, len(scored))
	for i, c := range scored {
		rows[i] = BountyRow{
			Rank: i + 1, GUID: c.GUID, Name: c.Name, KB: c.KB, Assists: c.Assists,
			Score: c.KB*BountyPointsPerKillingBlow + c.Assists*BountyPointsPerAssist,
		}
	}
	return rows
}
