package jobs

import (
	"context"
	"encoding/json"
	"fmt"
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
}

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
	for !state.Complete {
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
		for _, user := range page.Users {
			if user.ID != "" && !seenUsers[user.ID] {
				state.Users = append(state.Users, user)
				seenUsers[user.ID] = true
			}
		}
		state.Cursor, state.ViewerKey = page.Cursor, page.ViewerKey
		state.Complete = page.Cursor == ""
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
