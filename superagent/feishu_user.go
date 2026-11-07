package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// A person can authorize a channel's app as themselves once, from the
// Console, with Feishu's device flow: the Console shows a link and QR code,
// they approve in Feishu, and the daemon keeps the resulting token. With it
// the Console lists that person's groups and the people they have chatted
// with, and adds the channel's bot to one of their groups, so the bot needs no
// scope to read groups or people it does not already know.

const (
	feishuAccountsBase = "https://accounts.feishu.cn"
	larkAccountsBase   = "https://accounts.larksuite.com"
	feishuDeviceGrant  = "urn:ietf:params:oauth:grant-type:device_code"
	// A user token is refreshed this long before Feishu says it expires.
	feishuTokenMargin = 2 * time.Minute
	// Feishu's people search returns at most 30 a page.
	contactPageSize = 30
	maxContactIDs   = 100
	maxContactQuery = 50
	// The person's groups and 1:1 chats are read most recently active first,
	// and only this many pages of 100: past that they search.
	maxUserChatPages   = 3
	maxRecentChatPages = 2
	chatBatchSize      = 50 // Feishu's limit for chats/batch_query.
	maxChatLookup      = 100
	maxChatQuery       = 64
	chatSearchPageSize = 50
)

// FeishuUserScopes are what a person grants when they authorize a channel's
// app: reading their groups, adding a member to one, and searching the people
// they can see. offline_access lets AgentMux refresh the token instead of
// asking again every two hours.
var FeishuUserScopes = []string{"im:chat:read", "im:chat.members:write_only", "contact:user:search", "offline_access"}

// ErrFeishuUserAuthRequired means nobody has authorized the channel's app as
// themselves, or that authorization has lapsed.
var ErrFeishuUserAuthRequired = errors.New("authorize with your Feishu account first")

var errFeishuGrantsUnavailable = errors.New("Feishu user authorization is unavailable")

var feishuChatIDPattern = regexp.MustCompile(`^oc_[A-Za-z0-9_-]+$`)

// FeishuGrantStore keeps one user authorization per channel.
type FeishuGrantStore interface {
	GetFeishuUserGrant(ctx context.Context, channelID string) (*core.FeishuUserGrant, error)
	PutFeishuUserGrant(ctx context.Context, grant core.FeishuUserGrant) error
	DeleteFeishuUserGrant(ctx context.Context, channelID string) error
}

// FeishuUserDirectory is what the Console does as the person who authorized a
// channel's app.
type FeishuUserDirectory interface {
	StartUserAuth(ctx context.Context, channelID string) (FeishuUserAuthStart, error)
	PollUserAuth(ctx context.Context, channelID, deviceCode string) (FeishuUserAuthPoll, error)
	UserAuth(ctx context.Context, channelID string) (*FeishuUserAuth, error)
	ForgetUserAuth(ctx context.Context, channelID string) error
	// UserChats lists the person's groups, most recently active first, marking
	// those the bot is in.
	UserChats(ctx context.Context, channelID string) (FeishuChatList, error)
	// FindUserChats searches the person's groups for query, or names chatIDs.
	FindUserChats(ctx context.Context, channelID, query string, chatIDs []string) (FeishuChatList, error)
	AddBotToChat(ctx context.Context, channelID, chatID string) (FeishuAddBotResult, error)
	// UserContacts lists the people the person has chatted with one to one,
	// most recent first, searches everyone they can see for query, or looks
	// up openIDs.
	UserContacts(ctx context.Context, channelID, query string, openIDs []string) (FeishuUserList, error)
}

// FeishuUserAuthStart is an authorization waiting for the person to approve
// it at VerificationURL. DeviceCode stays in the daemon: it is what redeems
// the approval for a token.
type FeishuUserAuthStart struct {
	DeviceCode      string `json:"-"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Statuses of a pending authorization.
const (
	FeishuAuthPending   = "pending"
	FeishuAuthSlowDown  = "slow_down"
	FeishuAuthCompleted = "completed"
	FeishuAuthDenied    = "denied"
	FeishuAuthExpired   = "expired"
)

type FeishuUserAuthPoll struct {
	Status string          `json:"status"`
	User   *FeishuUserAuth `json:"user,omitempty"`
}

// FeishuUserAuth is who authorized a channel's app, without their tokens.
// Missing lists scopes their authorization lacks, usually because the app had
// not enabled them yet; ApplyURL is where to enable them, after which the
// person authorizes again.
type FeishuUserAuth struct {
	OpenID   string   `json:"open_id"`
	Name     string   `json:"name,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Missing  []string `json:"missing,omitempty"`
	ApplyURL string   `json:"apply_url,omitempty"`
}

type FeishuAddBotResult struct {
	Added bool `json:"added"`
	// Pending means the group's owner or an admin must approve new members.
	Pending bool `json:"pending,omitempty"`
}

// feishuOAuthToken is the token endpoint's answer, which carries OAuth error
// names, and sometimes a Feishu code, instead of a token.
type feishuOAuthToken struct {
	Code             int    `json:"code"`
	Msg              string `json:"msg"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (t feishuOAuthToken) problem() string {
	if text := firstNonEmpty(t.ErrorDescription, t.Error, t.Msg); text != "" {
		return text
	}
	if t.Code != 0 {
		return fmt.Sprintf("code %d", t.Code)
	}
	return "no access token"
}

// lapsed says the refresh token can no longer be used, so the person has to
// authorize again. Feishu reports this as invalid_grant or with these codes.
func (t feishuOAuthToken) lapsed() bool {
	switch t.Code {
	case 20026, 20037, 20064, 20073, 20074:
		return true
	}
	return t.Error == "invalid_grant" || t.Error == "expired_token"
}

func (f *FeishuAPI) StartUserAuth(ctx context.Context, channelID string) (FeishuUserAuthStart, error) {
	app, err := f.app(ctx, channelID)
	if err != nil {
		return FeishuUserAuthStart{}, err
	}
	form := url.Values{"client_id": {app.id}, "scope": {strings.Join(FeishuUserScopes, " ")}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.accounts+"/oauth/v1/device_authorization", strings.NewReader(form.Encode()))
	if err != nil {
		return FeishuUserAuthStart{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(app.id, app.secret)
	var result struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	if err := f.send(request, &result); err != nil {
		return FeishuUserAuthStart{}, fmt.Errorf("Feishu authorization: %w", err)
	}
	link := firstNonEmpty(result.VerificationURIComplete, result.VerificationURI)
	if result.Error != "" || result.DeviceCode == "" || !strings.HasPrefix(link, "https://") {
		return FeishuUserAuthStart{}, fmt.Errorf("Feishu authorization: %s", firstNonEmpty(result.ErrorDescription, result.Error, "Feishu returned no authorization link"))
	}
	start := FeishuUserAuthStart{DeviceCode: result.DeviceCode, UserCode: result.UserCode, VerificationURL: link, ExpiresIn: result.ExpiresIn, Interval: result.Interval}
	if start.ExpiresIn <= 0 {
		start.ExpiresIn = 240
	}
	if start.Interval <= 0 {
		start.Interval = 5
	}
	return start, nil
}

// PollUserAuth asks Feishu once whether the person has approved, and keeps
// their token when they have.
func (f *FeishuAPI) PollUserAuth(ctx context.Context, channelID, deviceCode string) (FeishuUserAuthPoll, error) {
	if f.Grants == nil {
		return FeishuUserAuthPoll{}, errFeishuGrantsUnavailable
	}
	app, err := f.app(ctx, channelID)
	if err != nil {
		return FeishuUserAuthPoll{}, err
	}
	form := url.Values{"grant_type": {feishuDeviceGrant}, "device_code": {deviceCode}, "client_id": {app.id}, "client_secret": {app.secret}}
	token, err := f.oauthToken(ctx, app, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return FeishuUserAuthPoll{}, err
	}
	if token.AccessToken == "" {
		switch token.Error {
		case "authorization_pending":
			return FeishuUserAuthPoll{Status: FeishuAuthPending}, nil
		case "slow_down":
			return FeishuUserAuthPoll{Status: FeishuAuthSlowDown}, nil
		case "access_denied":
			return FeishuUserAuthPoll{Status: FeishuAuthDenied}, nil
		case "expired_token", "invalid_grant":
			return FeishuUserAuthPoll{Status: FeishuAuthExpired}, nil
		}
		return FeishuUserAuthPoll{}, fmt.Errorf("Feishu authorization: %s", token.problem())
	}
	var user struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID string `json:"open_id"`
			Name   string `json:"name"`
			EnName string `json:"en_name"`
		} `json:"data"`
	}
	if err := f.call(ctx, http.MethodGet, app.base+"/open-apis/authen/v1/user_info", token.AccessToken, nil, &user); err != nil {
		return FeishuUserAuthPoll{}, fmt.Errorf("read the authorizing Feishu user: %w", err)
	}
	if user.Code != 0 || user.Data.OpenID == "" {
		return FeishuUserAuthPoll{}, fmt.Errorf("read the authorizing Feishu user: %d %s", user.Code, user.Msg)
	}
	now := f.now()
	grant := core.FeishuUserGrant{
		ChannelID: channelID, OpenID: user.Data.OpenID, Name: firstNonEmpty(user.Data.Name, user.Data.EnName),
		Scopes: strings.Fields(token.Scope), UpdatedAt: now,
	}
	applyToken(&grant, token, now)
	if err := f.Grants.PutFeishuUserGrant(ctx, grant); err != nil {
		return FeishuUserAuthPoll{}, err
	}
	return FeishuUserAuthPoll{Status: FeishuAuthCompleted, User: describeGrant(app.channel, grant)}, nil
}

// UserAuth says who authorized the channel's app, or nil when nobody has or
// their authorization can no longer be refreshed.
func (f *FeishuAPI) UserAuth(ctx context.Context, channelID string) (*FeishuUserAuth, error) {
	if f.Grants == nil {
		return nil, nil
	}
	app, err := f.app(ctx, channelID)
	if err != nil {
		return nil, err
	}
	grant, err := f.Grants.GetFeishuUserGrant(ctx, channelID)
	if err != nil || grant == nil {
		return nil, err
	}
	if !f.usable(*grant) {
		return nil, f.Grants.DeleteFeishuUserGrant(ctx, channelID)
	}
	return describeGrant(app.channel, *grant), nil
}

// ForgetUserAuth drops the channel's user token. Feishu keeps it valid until
// it expires, at most two hours; the person can also remove the app's access
// in Feishu.
func (f *FeishuAPI) ForgetUserAuth(ctx context.Context, channelID string) error {
	if f.Grants == nil {
		return errFeishuGrantsUnavailable
	}
	return f.Grants.DeleteFeishuUserGrant(ctx, channelID)
}

func (f *FeishuAPI) UserChats(ctx context.Context, channelID string) (list FeishuChatList, err error) {
	defer func() { err = f.explainUser(ctx, channelID, err) }()
	app, token, err := f.userToken(ctx, channelID)
	if err != nil {
		return FeishuChatList{}, err
	}
	items, truncated, err := feishuPagesUpTo[feishuChatRecord](ctx, f, app.base+"/open-apis/im/v1/chats?sort_type=ByActiveTimeDesc&"+feishuPageSizeArgs, token, maxUserChatPages)
	if err != nil {
		return FeishuChatList{}, err
	}
	list = FeishuChatList{Items: feishuChatsFrom(items), Truncated: truncated}
	f.markBotIn(ctx, app, list.Items)
	return list, nil
}

// FindUserChats searches the person's groups, by group or member name, or
// names the given chat ids. Public groups they have not joined are left out:
// they could not add the bot there.
func (f *FeishuAPI) FindUserChats(ctx context.Context, channelID, query string, chatIDs []string) (list FeishuChatList, err error) {
	defer func() { err = f.explainUser(ctx, channelID, err) }()
	query = strings.TrimSpace(query)
	var ids []string
	for _, id := range normalizeActors(chatIDs) {
		if feishuChatIDPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxChatLookup {
		return FeishuChatList{}, fmt.Errorf("look up at most %d chats at a time", maxChatLookup)
	}
	if len([]rune(query)) > maxChatQuery {
		return FeishuChatList{}, fmt.Errorf("search for at most %d characters", maxChatQuery)
	}
	list = FeishuChatList{Items: []FeishuChat{}}
	if len(ids) == 0 && (len(chatIDs) > 0 || query == "") {
		return list, nil
	}
	app, token, err := f.userToken(ctx, channelID)
	if err != nil {
		return FeishuChatList{}, err
	}
	var records []feishuChatRecord
	if len(ids) > 0 {
		for start := 0; start < len(ids); start += chatBatchSize {
			payload, _ := json.Marshal(map[string]any{"chat_ids": ids[start:min(start+chatBatchSize, len(ids))]})
			var result struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data struct {
					Items []feishuChatRecord `json:"items"`
				} `json:"data"`
			}
			if err := f.call(ctx, http.MethodPost, app.base+"/open-apis/im/v1/chats/batch_query?user_id_type=open_id", token, payload, &result); err != nil {
				return FeishuChatList{}, err
			}
			if result.Code != 0 {
				return FeishuChatList{}, feishuError(result.Code, result.Msg)
			}
			records = append(records, result.Data.Items...)
		}
	} else {
		// Feishu's search wants a hyphenated keyword quoted.
		if strings.Contains(query, "-") && !strings.HasPrefix(query, `"`) {
			query = `"` + query + `"`
		}
		payload, _ := json.Marshal(map[string]any{"query": query, "filter": map[string]any{"search_types": []string{"private", "external", "public_joined"}}})
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items []struct {
					MetaData feishuChatRecord `json:"meta_data"`
				} `json:"items"`
				HasMore bool `json:"has_more"`
			} `json:"data"`
		}
		target := fmt.Sprintf("%s/open-apis/im/v2/chats/search?page_size=%d", app.base, chatSearchPageSize)
		if err := f.call(ctx, http.MethodPost, target, token, payload, &result); err != nil {
			return FeishuChatList{}, err
		}
		if result.Code != 0 {
			return FeishuChatList{}, feishuError(result.Code, result.Msg)
		}
		for _, item := range result.Data.Items {
			records = append(records, item.MetaData)
		}
		list.Truncated = result.Data.HasMore
	}
	list.Items = feishuChatsFrom(records)
	f.markBotIn(ctx, app, list.Items)
	return list, nil
}

// feishuChatRecord is a chat as Feishu's chat listings, search and batch
// lookup describe it.
type feishuChatRecord struct {
	ChatID      string `json:"chat_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	External    bool   `json:"external"`
	ChatStatus  string `json:"chat_status"`
}

// feishuChatsFrom keeps each live chat once.
func feishuChatsFrom(records []feishuChatRecord) []FeishuChat {
	chats := make([]FeishuChat, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		if record.ChatID == "" || seen[record.ChatID] || strings.HasPrefix(record.ChatStatus, "dissolved") {
			continue
		}
		seen[record.ChatID] = true
		chats = append(chats, FeishuChat{ChatID: record.ChatID, Name: record.Name, Description: record.Description, External: record.External})
	}
	return chats
}

// markBotIn says which chats the bot is in already. The bot's own listing
// can fail, say for a missing app scope, and the chats still show; when it
// was cut short only the chats found in it are marked.
func (f *FeishuAPI) markBotIn(ctx context.Context, app feishuApp, chats []FeishuChat) {
	tenant, err := f.appToken(ctx, app)
	if err != nil {
		return
	}
	botChats, err := f.chats(ctx, app.base, tenant)
	if err != nil {
		return
	}
	in := map[string]bool{}
	for _, chat := range botChats.Items {
		in[chat.ChatID] = true
	}
	for index := range chats {
		if present := in[chats[index].ChatID]; present || !botChats.Truncated {
			chats[index].BotIn = &present
		}
	}
}

func (f *FeishuAPI) AddBotToChat(ctx context.Context, channelID, chatID string) (result FeishuAddBotResult, err error) {
	defer func() { err = f.explainUser(ctx, channelID, err) }()
	chatID = strings.TrimSpace(chatID)
	if !feishuChatIDPattern.MatchString(chatID) {
		return FeishuAddBotResult{}, fmt.Errorf("invalid chat_id %q", chatID)
	}
	app, token, err := f.userToken(ctx, channelID)
	if err != nil {
		return FeishuAddBotResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"id_list": []string{app.id}})
	target := fmt.Sprintf("%s/open-apis/im/v1/chats/%s/members?member_id_type=app_id", app.base, url.PathEscape(chatID))
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Invalid    []string `json:"invalid_id_list"`
			NotExisted []string `json:"not_existed_id_list"`
			Pending    []string `json:"pending_approval_id_list"`
		} `json:"data"`
	}
	if err := f.call(ctx, http.MethodPost, target, token, payload, &response); err != nil {
		return FeishuAddBotResult{}, err
	}
	if response.Code != 0 {
		return FeishuAddBotResult{}, feishuError(response.Code, response.Msg)
	}
	switch {
	case slices.Contains(response.Data.Pending, app.id):
		return FeishuAddBotResult{Pending: true}, nil
	case slices.Contains(response.Data.Invalid, app.id), slices.Contains(response.Data.NotExisted, app.id):
		return FeishuAddBotResult{}, errors.New("Feishu did not add the bot: publish the app and make it available to you, then try again")
	}
	return FeishuAddBotResult{Added: true}, nil
}

func (f *FeishuAPI) UserContacts(ctx context.Context, channelID, query string, openIDs []string) (list FeishuUserList, err error) {
	defer func() { err = f.explainUser(ctx, channelID, err) }()
	query = strings.TrimSpace(query)
	var ids []string
	for _, id := range normalizeActors(openIDs) {
		if strings.HasPrefix(id, "ou_") {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxContactIDs {
		return FeishuUserList{}, fmt.Errorf("look up at most %d people at a time", maxContactIDs)
	}
	if len([]rune(query)) > maxContactQuery {
		return FeishuUserList{}, fmt.Errorf("search for at most %d characters", maxContactQuery)
	}
	if len(openIDs) > 0 && len(ids) == 0 {
		return FeishuUserList{Items: []FeishuUser{}}, nil
	}
	app, token, err := f.userToken(ctx, channelID)
	if err != nil {
		return FeishuUserList{}, err
	}
	switch {
	case len(ids) > 0:
		return f.searchPeople(ctx, app, token, map[string]any{"filter": map[string]any{"user_ids": ids}}, (len(ids)+contactPageSize-1)/contactPageSize)
	case query != "":
		return f.searchPeople(ctx, app, token, map[string]any{"query": query}, 1)
	}
	return f.recentContacts(ctx, app, token)
}

// recentContacts is the people the person last talked to one to one, most
// recent first. The chats carry their names; emails and departments come from
// the people search where it can see them.
func (f *FeishuAPI) recentContacts(ctx context.Context, app feishuApp, token string) (FeishuUserList, error) {
	chats, more, err := feishuPagesUpTo[struct {
		Name       string `json:"name"`
		TargetID   string `json:"p2p_target_id"`
		TargetType string `json:"p2p_target_type"`
	}](ctx, f, app.base+"/open-apis/im/v1/chats?types=p2p&sort_type=ByActiveTimeDesc&user_id_type=open_id&"+feishuPageSizeArgs, token, maxRecentChatPages)
	if err != nil {
		return FeishuUserList{}, err
	}
	list := FeishuUserList{Items: []FeishuUser{}, Truncated: more}
	position := map[string]int{}
	for _, chat := range chats {
		if chat.TargetType != "user" || !strings.HasPrefix(chat.TargetID, "ou_") {
			continue // bots, and chats with no one else
		}
		if _, seen := position[chat.TargetID]; seen {
			continue
		}
		position[chat.TargetID] = len(list.Items)
		list.Items = append(list.Items, FeishuUser{OpenID: chat.TargetID, Name: strings.TrimSpace(chat.Name)})
	}
	for start := 0; start < len(list.Items); start += maxContactIDs {
		var ids []string
		for _, person := range list.Items[start:min(start+maxContactIDs, len(list.Items))] {
			ids = append(ids, person.OpenID)
		}
		found, err := f.searchPeople(ctx, app, token, map[string]any{"filter": map[string]any{"user_ids": ids}}, (len(ids)+contactPageSize-1)/contactPageSize)
		if err != nil {
			break // the names are enough to pick from
		}
		for _, person := range found.Items {
			if at, ok := position[person.OpenID]; ok {
				list.Items[at].Email, list.Items[at].Department = person.Email, person.Department
				list.Items[at].Name = firstNonEmpty(list.Items[at].Name, person.Name)
			}
		}
	}
	return list, nil
}

// searchPeople runs Feishu's people search as the person for up to pages
// pages of 30.
func (f *FeishuAPI) searchPeople(ctx context.Context, app feishuApp, token string, body map[string]any, pages int) (FeishuUserList, error) {
	payload, _ := json.Marshal(body)
	list := FeishuUserList{Items: []FeishuUser{}}
	pageToken := ""
	for range pages {
		target := fmt.Sprintf("%s/open-apis/contact/v3/users/search?page_size=%d", app.base, contactPageSize)
		if pageToken != "" {
			target += "&page_token=" + url.QueryEscape(pageToken)
		}
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items []struct {
					ID          string `json:"id"`
					DisplayInfo string `json:"display_info"`
					MetaData    struct {
						I18nNames             map[string]string `json:"i18n_names"`
						MailAddress           string            `json:"mail_address"`
						EnterpriseMailAddress string            `json:"enterprise_mail_address"`
					} `json:"meta_data"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}
		if err := f.call(ctx, http.MethodPost, target, token, payload, &result); err != nil {
			return FeishuUserList{}, err
		}
		if result.Code != 0 {
			return FeishuUserList{}, feishuError(result.Code, result.Msg)
		}
		for _, item := range result.Data.Items {
			if item.ID == "" {
				continue
			}
			list.Items = append(list.Items, FeishuUser{
				OpenID:     item.ID,
				Name:       feishuPersonName(item.MetaData.I18nNames, app.channel.Type),
				Email:      firstNonEmpty(item.MetaData.EnterpriseMailAddress, item.MetaData.MailAddress),
				Department: feishuDepartment(item.DisplayInfo),
			})
		}
		if !result.Data.HasMore || result.Data.PageToken == "" {
			return list, nil
		}
		pageToken = result.Data.PageToken
	}
	list.Truncated = true
	return list, nil
}

// userToken returns a current token of the person who authorized the
// channel's app, refreshing it when it is about to expire. Refreshing gives a
// new refresh token and retires the old one, so a channel refreshes one call
// at a time.
func (f *FeishuAPI) userToken(ctx context.Context, channelID string) (feishuApp, string, error) {
	if f.Grants == nil {
		return feishuApp{}, "", errFeishuGrantsUnavailable
	}
	app, err := f.app(ctx, channelID)
	if err != nil {
		return feishuApp{}, "", err
	}
	lock := f.grantLock(channelID)
	lock.Lock()
	defer lock.Unlock()
	grant, err := f.Grants.GetFeishuUserGrant(ctx, channelID)
	if err != nil {
		return app, "", err
	}
	if grant == nil || grant.AccessToken == "" {
		return app, "", ErrFeishuUserAuthRequired
	}
	now := f.now()
	if now.Before(grant.AccessExpiresAt.Add(-feishuTokenMargin)) {
		return app, grant.AccessToken, nil
	}
	if !f.usable(*grant) {
		_ = f.Grants.DeleteFeishuUserGrant(ctx, channelID)
		return app, "", ErrFeishuUserAuthRequired
	}
	payload, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": grant.RefreshToken, "client_id": app.id, "client_secret": app.secret})
	token, err := f.oauthToken(ctx, app, bytes.NewReader(payload), "application/json; charset=utf-8")
	if err != nil {
		return app, "", err
	}
	if token.AccessToken == "" {
		if token.lapsed() {
			_ = f.Grants.DeleteFeishuUserGrant(ctx, channelID)
			return app, "", ErrFeishuUserAuthRequired
		}
		return app, "", fmt.Errorf("refresh Feishu authorization: %s", token.problem())
	}
	applyToken(grant, token, now)
	if scopes := strings.Fields(token.Scope); len(scopes) > 0 {
		grant.Scopes = scopes
	}
	grant.UpdatedAt = now
	if err := f.Grants.PutFeishuUserGrant(ctx, *grant); err != nil {
		return app, "", err
	}
	return app, grant.AccessToken, nil
}

// usable says the grant still has a current token or one way to get one.
func (f *FeishuAPI) usable(grant core.FeishuUserGrant) bool {
	now := f.now()
	if now.Before(grant.AccessExpiresAt.Add(-feishuTokenMargin)) {
		return true
	}
	return grant.RefreshToken != "" && (grant.RefreshExpiresAt.IsZero() || now.Before(grant.RefreshExpiresAt))
}

func applyToken(grant *core.FeishuUserGrant, token feishuOAuthToken, now time.Time) {
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	grant.AccessToken = token.AccessToken
	grant.AccessExpiresAt = now.Add(time.Duration(expiresIn) * time.Second)
	if token.RefreshToken != "" {
		grant.RefreshToken = token.RefreshToken
		grant.RefreshExpiresAt = time.Time{}
		if token.RefreshExpiresIn > 0 {
			grant.RefreshExpiresAt = now.Add(time.Duration(token.RefreshExpiresIn) * time.Second)
		}
	}
}

func (f *FeishuAPI) oauthToken(ctx context.Context, app feishuApp, body io.Reader, contentType string) (feishuOAuthToken, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.accounts+"/oauth/v3/token", body)
	if err != nil {
		return feishuOAuthToken{}, err
	}
	request.Header.Set("Content-Type", contentType)
	var token feishuOAuthToken
	if err := f.send(request, &token); err != nil {
		return feishuOAuthToken{}, fmt.Errorf("Feishu token: %w", err)
	}
	return token, nil
}

// explainUser adds the apply link to a missing scope, and handles Feishu
// refusing the person's token.
func (f *FeishuAPI) explainUser(ctx context.Context, channelID string, err error) error {
	switch feishuCodeOf(err) {
	case 99991668, 99991677:
		return f.tokenRejected(ctx, channelID, err)
	}
	return f.explainDenied(ctx, channelID, err)
}

// tokenRejected drops an access token Feishu no longer accepts, so the next
// call refreshes it. Only a grant that cannot refresh asks the person to
// authorize again.
func (f *FeishuAPI) tokenRejected(ctx context.Context, channelID string, err error) error {
	if f.Grants == nil {
		return err
	}
	lock := f.grantLock(channelID)
	lock.Lock()
	defer lock.Unlock()
	grant, getErr := f.Grants.GetFeishuUserGrant(ctx, channelID)
	if getErr != nil || grant == nil {
		return fmt.Errorf("%w (%v)", ErrFeishuUserAuthRequired, err)
	}
	grant.AccessExpiresAt = time.Time{}
	if !f.usable(*grant) {
		_ = f.Grants.DeleteFeishuUserGrant(ctx, channelID)
		return fmt.Errorf("%w (%v)", ErrFeishuUserAuthRequired, err)
	}
	if putErr := f.Grants.PutFeishuUserGrant(ctx, *grant); putErr != nil {
		return putErr
	}
	return fmt.Errorf("Feishu refused the saved token, so it will be refreshed; try again: %w", err)
}

func describeGrant(channel *core.Channel, grant core.FeishuUserGrant) *FeishuUserAuth {
	auth := &FeishuUserAuth{OpenID: grant.OpenID, Name: grant.Name, Scopes: grant.Scopes}
	// Feishu lists what it granted; with no list nothing is known missing.
	if len(grant.Scopes) > 0 {
		for _, scope := range FeishuUserScopes {
			if !slices.Contains(grant.Scopes, scope) {
				auth.Missing = append(auth.Missing, scope)
			}
		}
	}
	if len(auth.Missing) > 0 {
		auth.ApplyURL = FeishuScopeApplyURL(channel, auth.Missing, feishuUserToken)
	}
	return auth
}

func (f *FeishuAPI) grantLock(channelID string) *sync.Mutex {
	f.grantMu.Lock()
	defer f.grantMu.Unlock()
	if f.grantLocks == nil {
		f.grantLocks = map[string]*sync.Mutex{}
	}
	lock := f.grantLocks[channelID]
	if lock == nil {
		lock = &sync.Mutex{}
		f.grantLocks[channelID] = lock
	}
	return lock
}

func (f *FeishuAPI) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// feishuPersonName picks the name in the tenant's usual language, then any.
func feishuPersonName(names map[string]string, channelType string) string {
	preferred := []string{"zh_cn", "en_us"}
	if channelType == "lark" {
		preferred = []string{"en_us", "zh_cn"}
	}
	for _, locale := range preferred {
		if name := strings.TrimSpace(names[locale]); name != "" {
			return name
		}
	}
	locales := make([]string, 0, len(names))
	for locale := range names {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		if name := strings.TrimSpace(names[locale]); name != "" {
			return name
		}
	}
	return ""
}

var feishuMarkup = regexp.MustCompile(`<[^>]*>`)

// feishuDepartment reads the department from a search result's display
// text, whose second line names it.
func feishuDepartment(displayInfo string) string {
	lines := strings.Split(displayInfo, "\n")
	if len(lines) < 2 {
		return ""
	}
	return strings.TrimSpace(feishuMarkup.ReplaceAllString(lines[1], ""))
}
