package jobs

import (
	"sort"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// selectArchiveBatch 按“最久未做全文归档”优先挑选最多 limit 个用户。
// 没有 tweet_sync 行的用户视为从未归档，排在最前；时间相同保持入参顺序。
func selectArchiveBatch(users []xapi.User, syncedAt map[string]time.Time, limit int) []xapi.User {
	if limit <= 0 || len(users) == 0 {
		return users
	}
	candidates := make([]xapi.User, 0, len(users))
	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if user.ID == "" || user.Blocking || user.Muting {
			continue
		}
		if _, ok := seen[user.ID]; ok {
			continue
		}
		seen[user.ID] = struct{}{}
		candidates = append(candidates, user)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, leftOK := syncedAt[candidates[i].ID]
		right, rightOK := syncedAt[candidates[j].ID]
		leftMissing := !leftOK || left.IsZero()
		rightMissing := !rightOK || right.IsZero()
		if leftMissing != rightMissing {
			return leftMissing
		}
		if leftMissing {
			return false
		}
		return left.Before(right)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}
