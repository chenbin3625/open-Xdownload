package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xclient"
)

type memberCheckpoint struct {
	Users       []xclient.User `json:"users"`
	Cursor      string         `json:"cursor"`
	ViewerKey   string         `json:"viewerKey"`
	SeenCursors []string       `json:"seenCursors"`
	Complete    bool           `json:"complete"`
	// EmptyPages 记录连续没有新增用户的页数，持久化后分段恢复仍能累计。
	EmptyPages int `json:"emptyPages,omitempty"`
}

// memberPageDelay 是成员分页之间的间隔，测试中可置零。
var memberPageDelay = xclient.PaginationPageDelay

func (m *Manager) collectArchiveMembers(ctx, saveCtx context.Context, job storage.Job, fetch func(string, string) (xclient.UserPage, error)) ([]xclient.User, error) {
	cp, _, err := m.loadArchiveCheckpoint(saveCtx, job.ID)
	if err != nil {
		return nil, err
	}
	if cp.Members == nil {
		cp.Members = &memberCheckpoint{}
	}
	state := cp.Members
	seenUsers, seenCursors := map[string]bool{}, map[string]bool{}
	for _, user := range state.Users {
		seenUsers[user.ID] = true
	}
	for _, cursor := range state.SeenCursors {
		seenCursors[cursor] = true
	}
	started := time.Now()
	for fetched := 0; !state.Complete; fetched++ {
		if fetched > 0 && memberPageDelay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(memberPageDelay):
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(state.SeenCursors) >= 1000 {
			return nil, fmt.Errorf("成员列表超过安全页数上限")
		}
		page, err := fetch(state.Cursor, state.ViewerKey)
		if err != nil {
			return nil, err
		}
		if page.Cursor != "" && (page.Cursor == state.Cursor || seenCursors[page.Cursor]) {
			return nil, fmt.Errorf("成员列表游标未前进")
		}
		before := len(state.Users)
		for _, user := range page.Users {
			if user.ID != "" && !seenUsers[user.ID] {
				state.Users = append(state.Users, user)
				seenUsers[user.ID] = true
			}
		}
		state.Cursor, state.ViewerKey = page.Cursor, page.ViewerKey
		state.Complete = page.Cursor == ""
		// X 翻到末尾后仍会返回不断前进的 Bottom cursor（如 "0|..."）但不再有用户；
		// 不加判断会一直翻到安全页数上限并让整个任务失败。
		if len(state.Users) > before {
			state.EmptyPages = 0
		} else if !state.Complete {
			state.EmptyPages++
			if state.EmptyPages >= xclient.MaxConsecutiveEmptyMemberPages {
				log.Printf("jobs: 成员列表连续 %d 页没有新增用户，视为已到末尾 (job=%d, users=%d)", state.EmptyPages, job.ID, len(state.Users))
				state.Complete = true
			}
		}
		if page.Cursor != "" {
			state.SeenCursors = append(state.SeenCursors, page.Cursor)
			seenCursors[page.Cursor] = true
		}
		payload, err := json.Marshal(cp)
		if err != nil {
			return nil, err
		}
		if err := m.store.SaveJobCheckpoint(saveCtx, job.ID, payload); err != nil {
			return nil, err
		}
		if !state.Complete && time.Since(started) >= archiveSliceBudget {
			return nil, errYieldExecution
		}
	}
	return state.Users, nil
}
