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

// feishuScopeDenied is Feishu's code for a call the app has no scope for.
const feishuScopeDenied = 99991672

var (
	feishuScopeList = regexp.MustCompile(`\[([^\]]*)\]`)
	feishuScopeName = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	feishuAppID     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// FeishuScopeError is Feishu refusing a call because the channel's app lacks a
// scope. Scopes lists the ones Feishu would accept; any one is enough.
// ApplyURL opens the app's permission page in the developer console with
// those scopes picked, so the operator can request them.
type FeishuScopeError struct {
	Code     int
	Msg      string
	Scopes   []string
	ApplyURL string
}

func (e *FeishuScopeError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Msg) }

// feishuError turns a non-zero Feishu result into an error, typed when it is a
// missing scope.
func feishuError(code int, msg string) error {
	if code != feishuScopeDenied {
		return fmt.Errorf("%d %s", code, msg)
	}
	return &FeishuScopeError{Code: code, Msg: msg, Scopes: feishuScopesIn(msg)}
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
// requests scopes. With no scopes it opens the app's permission list.
func FeishuScopeApplyURL(channel *core.Channel, scopes []string) string {
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
	return link + "?q=" + strings.Join(scopes, ",") + "&op_from=openapi&token_type=tenant"
}

// explainDenied adds the apply link to a missing-scope error from the
// channel's app. Any other error is returned as it is.
func (f *FeishuAPI) explainDenied(ctx context.Context, channelID string, err error) error {
	var denied *FeishuScopeError
	if !errors.As(err, &denied) || denied.ApplyURL != "" {
		return err
	}
	if channel, lookupErr := f.Channels(ctx, channelID); lookupErr == nil {
		denied.ApplyURL = FeishuScopeApplyURL(channel, denied.Scopes)
	}
	return err
}
