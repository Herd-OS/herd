package dashboard

import "sort"

// SortBatches orders batches: stable-disagreement first, then cascade-failed,
// then attention items, then by LatestActivity desc, then by MilestoneNumber
// asc. Sorts in place.
func SortBatches(batches []BatchEntry) {
	sort.SliceStable(batches, func(i, j int) bool {
		a, b := batches[i], batches[j]
		if a.StableDisagreement != b.StableDisagreement {
			return a.StableDisagreement
		}
		if a.CascadeFailed != b.CascadeFailed {
			return a.CascadeFailed
		}
		if a.HasAttention != b.HasAttention {
			return a.HasAttention
		}
		if !a.LatestActivity.Equal(b.LatestActivity) {
			return a.LatestActivity.After(b.LatestActivity)
		}
		return a.MilestoneNumber < b.MilestoneNumber
	})
}
