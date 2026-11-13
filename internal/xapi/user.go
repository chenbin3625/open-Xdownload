package xapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// LookupFetcher 抓取一次用户查询响应。last 是上一次因年龄限制被拒绝的响应
// （首次调用为 nil）；retry 表示实现还可以换一个账号继续尝试。为 nil 时直接
// 使用 Requester.GraphQL。账号轮换、排除当前账号等策略由各实现放在这里。
type LookupFetcher func(ctx context.Context, path string, values url.Values, last []byte) (payload []byte, retry bool, err error)

// GetUserByInput 按 ID 或 @screen_name 查询用户。
func GetUserByInput(ctx context.Context, requester Requester, input string, fetch LookupFetcher) (User, error) {
	input = strings.TrimSpace(strings.TrimPrefix(input, "@"))
	if input == "" {
		return User{}, errors.New("用户不能为空")
	}
	if _, err := strconv.ParseUint(input, 10, 64); err == nil {
		return GetUserByID(ctx, requester, input, fetch)
	}
	return GetUserByScreenName(ctx, requester, input, fetch)
}

// GetUserByID 按 rest_id 查询用户。
func GetUserByID(ctx context.Context, requester Requester, id string, fetch LookupFetcher) (User, error) {
	payload, err := lookupUserPayload(ctx, requester, UserByRestIDPath, UserByIDValues(id), fetch)
	if err != nil {
		return User{}, err
	}
	user, err := ParseUser(gjson.GetBytes(payload, "data.user"))
	if err != nil {
		return User{}, fmt.Errorf("查询用户 ID %s 失败: %w", id, err)
	}
	return user, nil
}

// GetUserByScreenName 按 screen_name 查询用户。
func GetUserByScreenName(ctx context.Context, requester Requester, screenName string, fetch LookupFetcher) (User, error) {
	payload, err := lookupUserPayload(ctx, requester, UserByScreenNamePath, UserByScreenNameValues(screenName), fetch)
	if err != nil {
		return User{}, err
	}
	user, err := ParseUser(gjson.GetBytes(payload, "data.user"))
	if err != nil {
		return User{}, fmt.Errorf("查询用户 @%s 失败: %w", screenName, err)
	}
	return user, nil
}

// lookupUserPayload 查询用户数据。fetch 非空时（Pool 实现）可在目标账号受年龄
// 限制时排除当前 Cookie 并换号重试：请求本身是 HTTP 200，底层不会自动换号，
// 而不同账号的年龄验证状态、登录地区不同，换号可能就成功。
// 所有 Cookie 都被拒绝时返回最后一次响应，由 ParseUser 生成带提示的错误。
func lookupUserPayload(ctx context.Context, requester Requester, path string, values url.Values, fetch LookupFetcher) ([]byte, error) {
	if fetch == nil {
		return requester.GraphQL(ctx, path, values)
	}
	var last []byte
	for {
		payload, retry, err := fetch(ctx, path, values, last)
		if err != nil {
			return nil, err
		}
		if !AgeRestrictedPayload(payload) || !retry {
			return payload, nil
		}
		last = payload
	}
}

// ListFetcher 抓取一次列表元信息响应，并返回读取该列表的账号的 ViewerKey。
type ListFetcher func(ctx context.Context, path string, values url.Values) (payload []byte, viewerKey string, err error)

// GetListByID 查询列表元信息。fetch 为 nil 时直接使用 Requester.GraphQL。
func GetListByID(ctx context.Context, requester Requester, id string, fetch ListFetcher) (List, error) {
	values := ListByIDValues(id)
	var payload []byte
	var viewerKey string
	var err error
	if fetch != nil {
		payload, viewerKey, err = fetch(ctx, ListByRestIDPath, values)
	} else {
		payload, err = requester.GraphQL(ctx, ListByRestIDPath, values)
	}
	if err != nil {
		return List{}, err
	}
	raw := gjson.GetBytes(payload, "data.list")
	if !raw.Exists() {
		return List{}, errors.New("列表不存在")
	}
	creator, _ := ParseUser(raw.Get("user_results"))
	return List{
		ID:          FirstString(raw.Get("id_str"), raw.Get("id")),
		Name:        raw.Get("name").String(),
		MemberCount: int(raw.Get("member_count").Int()),
		Creator:     creator,
		ViewerKey:   viewerKey,
	}, nil
}
