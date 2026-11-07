package superagent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Feishu's codes for a call made without a scope it needs: as the app with a
// tenant token, or as a person with a user token.
const (
	feishuScopeDenied     = 99991672
	feishuUserScopeDenied = 99991679
)

// Token types, as Feishu's developer console names them in its links.
const (
	feishuTenantToken = "tenant"
	feishuUserToken   = "user"
)

var (
	feishuScopeList = regexp.MustCompile(`\[([^\]]*)\]`)
	feishuScopeName = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	feishuAppID     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// FeishuScopeError is Feishu refusing a call because the channel's app lacks a
// scope. Scopes lists the ones Feishu would accept; any one is enough.
// ApplyURL opens the app's permission page in the developer console with
// those scopes picked, so the operator can request them. TokenType says
// whether the scope is missing for the app itself or for a person acting
// through it; a person must also authorize again once the app has it.
type FeishuScopeError struct {
	Code      int
	Msg       string
	Scopes    []string
	TokenType string
	ApplyURL  string
}

func (e *FeishuScopeError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Msg) }

// feishuError turns a non-zero Feishu result into an error, typed when it is a
// missing scope.
func feishuError(code int, msg string) error {
	switch code {
	case feishuScopeDenied:
		return &FeishuScopeError{Code: code, Msg: msg, Scopes: feishuScopesIn(msg), TokenType: feishuTenantToken}
	case feishuUserScopeDenied:
		return &FeishuScopeError{Code: code, Msg: msg, Scopes: feishuScopesIn(msg), TokenType: feishuUserToken}
	}
	return &feishuAPIError{Code: code, Msg: msg}
}

// feishuAPIError is any other non-zero Feishu result.
type feishuAPIError struct {
	Code int
	Msg  string
}

func (e *feishuAPIError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Msg) }

// feishuCodeOf is the Feishu code behind err, or 0.
func feishuCodeOf(err error) int {
	var api *feishuAPIError
	if errors.As(err, &api) {
		return api.Code
	}
	var denied *FeishuScopeError
	if errors.As(err, &denied) {
		return denied.Code
	}
	return 0
}

// feishuScopesIn reads the scopes from Feishu's message, which names them as
// "One of the following scopes is required: [a, b]".
func feishuScopesIn(msg string) []string {
	match := feishuScopeList.FindStringSubmatch(msg)
	if match == nil {
		return nil
	}
	var scopes []string
	for _, scope := range strings.Split(match[1], ",") {
		scope = strings.TrimSpace(scope)
		if feishuScopeName.MatchString(scope) && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// FeishuScopeApplyURL is the developer console page where the channel's app
// requests scopes, for itself (tokenType "tenant") or for the people who
// authorize it ("user"). With no scopes it opens the app's permission list.
func FeishuScopeApplyURL(channel *core.Channel, scopes []string, tokenType string) string {
	if channel == nil {
		return ""
	}
	appID := strings.TrimSpace(channel.Config["app_id"])
	if !feishuAppID.MatchString(appID) {
		return ""
	}
	console := feishuOpenBase
	if channel.Type == "lark" {
		console = larkOpenBase
	}
	link := console + "/app/" + url.PathEscape(appID) + "/auth"
	if len(scopes) == 0 {
		return link
	}
	// Scope names are checked against feishuScopeName, so they need no
	// escaping, and Feishu's own links keep ":" and "," readable.
	if tokenType != feishuUserToken {
		tokenType = feishuTenantToken
	}
	return link + "?q=" + strings.Join(scopes, ",") + "&op_from=openapi&token_type=" + tokenType
}

// explainDenied adds the apply link to a missing-scope error from the
// channel's app. Any other error is returned as it is.
func (f *FeishuAPI) explainDenied(ctx context.Context, channelID string, err error) error {
	var denied *FeishuScopeError
	if !errors.As(err, &denied) || denied.ApplyURL != "" {
		return err
	}
	if channel, lookupErr := f.Channels(ctx, channelID); lookupErr == nil {
		denied.ApplyURL = FeishuScopeApplyURL(channel, denied.Scopes, denied.TokenType)
	}
	return err
}
