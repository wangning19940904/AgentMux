package superagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// FeishuScopeChecker says which of the scopes AgentMux asks for a channel's
// app has.
type FeishuScopeChecker interface {
	ScopeStatus(ctx context.Context, channelID string, tenantScopes, userScopes []string) (FeishuScopeStatus, error)
}

// FeishuScopeStatus compares what a channel's app needs, for itself and for
// the people who authorize it, with what its tenant has granted.
type FeishuScopeStatus struct {
	Tenant FeishuScopeGap `json:"tenant"`
	User   FeishuScopeGap `json:"user"`
}

// FeishuScopeGap lists scopes the app has not requested yet, with the console
// link that requests them, and scopes requested but not granted, which wait
// for the tenant's admin.
type FeishuScopeGap struct {
	Missing  []string `json:"missing,omitempty"`
	Pending  []string `json:"pending,omitempty"`
	ApplyURL string   `json:"apply_url,omitempty"`
}

// feishuScopeGranted is Feishu's grant_status for a granted scope.
const feishuScopeGranted = 1

// ScopeStatus reads the app's scopes with its own tenant token, which needs no
// scope of its own. Feishu lists only scopes in a published version.
func (f *FeishuAPI) ScopeStatus(ctx context.Context, channelID string, tenantScopes, userScopes []string) (status FeishuScopeStatus, err error) {
	defer func() { err = f.explainDenied(ctx, channelID, err) }()
	app, err := f.app(ctx, channelID)
	if err != nil {
		return FeishuScopeStatus{}, err
	}
	token, err := f.appToken(ctx, app)
	if err != nil {
		return FeishuScopeStatus{}, err
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Scopes []struct {
				Name        string `json:"scope_name"`
				GrantStatus int    `json:"grant_status"`
				Type        string `json:"scope_type"`
			} `json:"scopes"`
		} `json:"data"`
	}
	if err := f.call(ctx, http.MethodGet, app.base+"/open-apis/application/v6/scopes", token, nil, &result); err != nil {
		return FeishuScopeStatus{}, err
	}
	if result.Code != 0 {
		return FeishuScopeStatus{}, feishuError(result.Code, result.Msg)
	}
	granted := map[string]map[string]int{feishuTenantToken: {}, feishuUserToken: {}}
	for _, scope := range result.Data.Scopes {
		if kind := granted[scope.Type]; kind != nil {
			kind[scope.Name] = scope.GrantStatus
		}
	}
	gap := func(needed []string, tokenType string) FeishuScopeGap {
		var out FeishuScopeGap
		for _, name := range normalizeActors(needed) {
			switch grant, ok := granted[tokenType][name]; {
			case !ok:
				out.Missing = append(out.Missing, name)
			case grant != feishuScopeGranted:
				out.Pending = append(out.Pending, name)
			}
		}
		if len(out.Missing) > 0 {
			out.ApplyURL = FeishuScopeApplyURL(app.channel, out.Missing, tokenType)
		}
		return out
	}
	return FeishuScopeStatus{Tenant: gap(tenantScopes, feishuTenantToken), User: gap(userScopes, feishuUserToken)}, nil
}
