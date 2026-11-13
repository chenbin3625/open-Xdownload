package xclient

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
)

// MaxConsecutiveEmptyMemberPages 供分段成员收集使用：X 翻到列表末尾后仍会持续返回
// 新的 Bottom cursor，连续这么多页没有新增用户即视为已到末尾。
const MaxConsecutiveEmptyMemberPages = maxConsecutiveEmptyPages

// PaginationPageDelay 是成员分页之间的请求间隔，与其他时间线分页保持一致。
const PaginationPageDelay = paginationPageDelay

type UserPage struct {
	Users     []User
	Cursor    string
	ViewerKey string
}

func (p *Pool) GetListMembersPage(ctx context.Context, list List, cursor, viewerKey string) (UserPage, error) {
	return p.memberPage(ctx, listMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": list.ID, "count": 200, "withSafetyModeUserFields": true}, cursor, viewerKey, list.viewer)
}

func (p *Pool) GetFollowingPage(ctx context.Context, user User, cursor, viewerKey string) (UserPage, error) {
	var preferred *Client
	if user.Protected {
		preferred = p.Primary()
		if preferred == nil {
			return UserPage{}, fmt.Errorf("没有可用 X 主账号")
		}
	}
	return p.memberPage(ctx, followingPath, "data.user.result.timeline.timeline.instructions",
		map[string]any{"userId": user.ID, "count": 200, "includePromotedContent": false}, cursor, viewerKey, preferred)
}

// A saved cursor is bound to the original viewer, including after a pool is recreated.
func (p *Pool) memberPage(ctx context.Context, path, instructions string, variables map[string]any, cursor, viewerKey string, client *Client) (UserPage, error) {
	if viewerKey != "" {
		client = nil
		for _, candidate := range p.clients {
			if fmt.Sprintf("%x", sha256.Sum256([]byte(candidate.authToken))) == viewerKey {
				client = candidate
				break
			}
		}
		if client == nil {
			return UserPage{}, fmt.Errorf("成员分页使用的 X 账号已移除，请重新创建归档任务")
		}
	}
	if client == nil {
		var err error
		client, err = p.Select(ctx, path)
		if err != nil {
			return UserPage{}, err
		}
	}
	variables["cursor"] = cursor
	values := url.Values{"variables": {encodeVariables(variables)}, "features": {timelineFeatures}}
	payload, err := client.graphQL(ctx, path, values)
	if err != nil {
		return UserPage{}, err
	}
	items, next, err := timelineItems(payload, instructions)
	if err != nil {
		return UserPage{}, err
	}
	page := UserPage{Cursor: next, ViewerKey: fmt.Sprintf("%x", sha256.Sum256([]byte(client.authToken)))}
	for _, item := range items {
		user, err := parseUser(item.Get("user_results"))
		if err == nil && user.ID != "" {
			page.Users = append(page.Users, user)
		}
	}
	return page, nil
}
