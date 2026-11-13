package xclient

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// viewerKeyForClient 是成员分页的账号绑定键：auth_token 的 sha256 十六进制。
// 该值会写进 jobs 的检查点，重建 Pool 后仍能找回同一账号继续分页。
func viewerKeyForClient(client *Client) string {
	if client == nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(client.authToken)))
}

func (p *Pool) clientByViewerKey(viewerKey string) *Client {
	for _, candidate := range p.clients {
		if viewerKeyForClient(candidate) == viewerKey {
			return candidate
		}
	}
	return nil
}

func (p *Pool) GetListMembersPage(ctx context.Context, list List, cursor, viewerKey string) (UserPage, error) {
	// 检查点里的 viewerKey 优先；首次请求时沿用读取列表元信息的账号。
	if viewerKey == "" {
		viewerKey = list.ViewerKey
	}
	return p.memberPage(ctx, listMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": list.ID, "count": 200, "withSafetyModeUserFields": true}, cursor, viewerKey, nil)
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
		client = p.clientByViewerKey(viewerKey)
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
	page, err := xapi.FetchUserPage(ctx, client, path, instructions, variables, cursor)
	if err != nil {
		return UserPage{}, err
	}
	page.ViewerKey = viewerKeyForClient(client)
	return page, nil
}
