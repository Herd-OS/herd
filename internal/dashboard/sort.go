package dashboard

import "sort"

// SortBatches orders batches: attention items first, then by LatestActivity desc,
// then by MilestoneNumber asc. Sorts in place.
func SortBatches(batches []BatchEntry) {
	sort.SliceStable(batches, func(i, j int) bool {
		a, b := batches[i], batches[j]
		if a.HasAttention != b.HasAttention {
			return a.HasAttention
		}
		if !a.LatestActivity.Equal(b.LatestActivity) {
			return a.LatestActivity.After(b.LatestActivity)
		}
		return a.MilestoneNumber < b.MilestoneNumber
	})
}
