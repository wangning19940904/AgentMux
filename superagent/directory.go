package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

const (
	maxDirectoryPages  = 10 // Feishu pages hold at most 100 items.
	maxMemberChats     = 30
	maxDirectoryUsers  = 2000
	maxUserLookup      = 50 // Feishu's batch limit for both contact lookups.
	directoryFetchers  = 4
	feishuPageSizeArgs = "page_size=100"
)

// FeishuChat is a group the channel's bot is in.
type FeishuChat struct {
	ChatID      string `json:"chat_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	External    bool   `json:"external,omitempty"`
}

// FeishuUser is a person the channel's app knows by open_id. ChatIDs lists the
// bot's groups they are in, when they were found through one.
type FeishuUser struct {
	OpenID  string   `json:"open_id"`
	Name    string   `json:"name,omitempty"`
	Email   string   `json:"email,omitempty"`
	ChatIDs []string `json:"chat_ids,omitempty"`
}

// FeishuChatList and FeishuUserList stop at a size the Console can show.
// Truncated says Feishu had more.
type FeishuChatList struct {
	Items     []FeishuChat `json:"items"`
	Truncated bool         `json:"truncated,omitempty"`
}

type FeishuUserList struct {
	Items     []FeishuUser `json:"items"`
	Truncated bool         `json:"truncated,omitempty"`
	// Warnings name the groups whose members could not be read.
	Warnings []string `json:"warnings,omitempty"`
}

// FeishuDirectory lists what a channel's bot can see, so a Source can be set
// up by picking groups and people rather than typing chat_id and open_id values.
type FeishuDirectory interface {
	Chats(ctx context.Context, channelID string) (FeishuChatList, error)
	// Members lists the people in the given groups, or in the bot's groups
	// when none are given.
	Members(ctx context.Context, channelID string, chatIDs []string) (FeishuUserList, error)
	// Users resolves open_ids to names and emails to open_ids.
	Users(ctx context.Context, channelID string, openIDs, emails []string) ([]FeishuUser, error)
}

func (f *FeishuAPI) Chats(ctx context.Context, channelID string) (FeishuChatList, error) {
	base, token, err := f.tenantToken(ctx, channelID)
	if err != nil {
		return FeishuChatList{}, err
	}
	list, err := f.chats(ctx, base, token)
	return list, f.explainDenied(ctx, channelID, err)
}

// chats uses the tenant token, which lists the groups the bot itself is in.
func (f *FeishuAPI) chats(ctx context.Context, base, token string) (FeishuChatList, error) {
	items, truncated, err := feishuPages[struct {
		ChatID      string `json:"chat_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		External    bool   `json:"external"`
	}](ctx, f, base+"/open-apis/im/v1/chats?"+feishuPageSizeArgs, token)
	if err != nil {
		return FeishuChatList{}, err
	}
	list := FeishuChatList{Items: make([]FeishuChat, 0, len(items)), Truncated: truncated}
	for _, item := range items {
		list.Items = append(list.Items, FeishuChat{ChatID: item.ChatID, Name: item.Name, Description: item.Description, External: item.External})
	}
	return list, nil
}

func (f *FeishuAPI) Members(ctx context.Context, channelID string, chatIDs []string) (list FeishuUserList, err error) {
	defer func() { err = f.explainDenied(ctx, channelID, err) }()
	base, token, err := f.tenantToken(ctx, channelID)
	if err != nil {
		return FeishuUserList{}, err
	}
	list = FeishuUserList{Items: []FeishuUser{}}
	if len(chatIDs) == 0 {
		chats, err := f.chats(ctx, base, token)
		if err != nil {
			return FeishuUserList{}, err
		}
		for _, chat := range chats.Items {
			chatIDs = append(chatIDs, chat.ChatID)
		}
		list.Truncated = chats.Truncated
	}
	if len(chatIDs) > maxMemberChats {
		chatIDs, list.Truncated = chatIDs[:maxMemberChats], true
	}

	type chatMember struct {
		MemberID string `json:"member_id"`
		Name     string `json:"name"`
	}
	type result struct {
		members   []chatMember
		truncated bool
		err       error
	}
	results := make([]result, len(chatIDs))
	limit := make(chan struct{}, directoryFetchers)
	var wg sync.WaitGroup
	for index, chatID := range chatIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			target := fmt.Sprintf("%s/open-apis/im/v1/chats/%s/members?member_id_type=open_id&%s", base, url.PathEscape(chatID), feishuPageSizeArgs)
			members, truncated, err := feishuPages[chatMember](ctx, f, target, token)
			results[index] = result{members, truncated, err}
		}()
	}
	wg.Wait()

	// One unreadable group, such as one the bot has left, should not hide the
	// rest. Only a failure on every group is an error.
	var firstErr error
	position := map[string]int{}
	for index, result := range results {
		chatID := chatIDs[index]
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			list.Warnings = append(list.Warnings, fmt.Sprintf("%s: %v", chatID, result.err))
			continue
		}
		list.Truncated = list.Truncated || result.truncated
		for _, member := range result.members {
			if member.MemberID == "" {
				continue
			}
			if at, seen := position[member.MemberID]; seen {
				list.Items[at].ChatIDs = append(list.Items[at].ChatIDs, chatID)
				continue
			}
			if len(list.Items) >= maxDirectoryUsers {
				list.Truncated = true
				continue
			}
			position[member.MemberID] = len(list.Items)
			list.Items = append(list.Items, FeishuUser{OpenID: member.MemberID, Name: member.Name, ChatIDs: []string{chatID}})
		}
	}
	if firstErr != nil && len(list.Warnings) == len(chatIDs) {
		return FeishuUserList{}, firstErr
	}
	return list, nil
}

// Users only finds people inside the app's contact scope. An email lookup
// still answers when names need a scope the app lacks.
func (f *FeishuAPI) Users(ctx context.Context, channelID string, openIDs, emails []string) (users []FeishuUser, err error) {
	defer func() { err = f.explainDenied(ctx, channelID, err) }()
	openIDs, emails = normalizeActors(openIDs), normalizeActors(emails)
	if len(openIDs) > maxUserLookup || len(emails) > maxUserLookup {
		return nil, fmt.Errorf("look up at most %d people at a time", maxUserLookup)
	}
	users = []FeishuUser{}
	if len(openIDs)+len(emails) == 0 {
		return users, nil
	}
	base, token, err := f.tenantToken(ctx, channelID)
	if err != nil {
		return nil, err
	}
	emailOf := map[string]string{}
	if len(emails) > 0 {
		payload, _ := json.Marshal(map[string]any{"emails": emails})
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				UserList []struct {
					UserID string `json:"user_id"`
					Email  string `json:"email"`
				} `json:"user_list"`
			} `json:"data"`
		}
		if err := f.call(ctx, http.MethodPost, base+"/open-apis/contact/v3/users/batch_get_id?user_id_type=open_id", token, payload, &result); err != nil {
			return nil, err
		}
		if result.Code != 0 {
			return nil, feishuError(result.Code, result.Msg)
		}
		for _, item := range result.Data.UserList {
			if item.UserID == "" {
				continue // Not found, or outside the app's contact scope.
			}
			emailOf[item.UserID] = item.Email
			if !slices.Contains(openIDs, item.UserID) {
				openIDs = append(openIDs, item.UserID)
			}
		}
	}
	if len(openIDs) == 0 {
		return users, nil
	}

	query := url.Values{"user_id_type": {"open_id"}, "user_ids": openIDs}
	var named struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				OpenID string `json:"open_id"`
				Name   string `json:"name"`
				Email  string `json:"email"`
			} `json:"items"`
		} `json:"data"`
	}
	err = f.call(ctx, http.MethodGet, base+"/open-apis/contact/v3/users/batch?"+query.Encode(), token, nil, &named)
	if err == nil && named.Code != 0 {
		err = feishuError(named.Code, named.Msg)
	}
	if err != nil {
		if len(emailOf) == 0 {
			return nil, err
		}
		for openID, email := range emailOf {
			users = append(users, FeishuUser{OpenID: openID, Email: email})
		}
		slices.SortFunc(users, func(a, b FeishuUser) int { return strings.Compare(a.Email, b.Email) })
		return users, nil
	}
	for _, item := range named.Data.Items {
		users = append(users, FeishuUser{OpenID: item.OpenID, Name: item.Name, Email: firstNonEmpty(item.Email, emailOf[item.OpenID])})
	}
	return users, nil
}

// feishuPages follows page_token pagination for up to maxDirectoryPages pages
// and reports whether Feishu had more.
func feishuPages[T any](ctx context.Context, f *FeishuAPI, target, token string) ([]T, bool, error) {
	var items []T
	pageToken := ""
	for range maxDirectoryPages {
		next := target
		if pageToken != "" {
			next += "&page_token=" + url.QueryEscape(pageToken)
		}
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items     []T    `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if err := f.call(ctx, http.MethodGet, next, token, nil, &result); err != nil {
			return nil, false, err
		}
		if result.Code != 0 {
			return nil, false, feishuError(result.Code, result.Msg)
		}
		items = append(items, result.Data.Items...)
		if !result.Data.HasMore || result.Data.PageToken == "" {
			return items, false, nil
		}
		pageToken = result.Data.PageToken
	}
	return items, true, nil
}
