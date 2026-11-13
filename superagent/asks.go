package superagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// An ask is a message the router posted somewhere else on behalf of a thread,
// usually a question for whoever can decide something. The answer arrives as
// an event in that other place, in a router conversation that never saw the
// question. AgentMux remembers each ask, shows it with events from that place
// and lets the router post the answer back to the thread that is waiting.

const (
	asksFile     = "asks.json"
	askTTL       = 72 * time.Hour
	maxAsksShown = 5
	maxAsks      = 200
)

type pendingAsk struct {
	At         time.Time       `json:"at"`
	Place      string          `json:"place"`
	Text       string          `json:"text"`
	FromThread string          `json:"from_thread"`
	FromSource string          `json:"from_source"`
	FromReply  json.RawMessage `json:"from_reply"`
}

// askView is an ask as the router is shown it.
type askView struct {
	Asked      string `json:"asked"`
	OnBehalfOf string `json:"on_behalf_of"`
	To         string `json:"to"`
	Text       string `json:"text"`
}

// eventPlace is the place a thread is in: a Feishu group, or a GitHub issue
// or pull request.
func eventPlace(threadKey string) string {
	if rest, ok := strings.CutPrefix(threadKey, "feishu:"); ok {
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) >= 2 {
			return "feishu:" + parts[0] + ":" + parts[1]
		}
	}
	return strings.ToLower(threadKey)
}

// destinationPlace is the place a reply address points at.
func destinationPlace(reply []byte) string {
	var feishu feishuReply
	if json.Unmarshal(reply, &feishu) == nil && feishu.ChatID != "" {
		return "feishu:" + feishu.ChannelID + ":" + feishu.ChatID
	}
	var mail mailReply
	if json.Unmarshal(reply, &mail) == nil && mail.MailTo != "" {
		return strings.ToLower(mailThreadKey(mail.MailTo))
	}
	var github githubReply
	if json.Unmarshal(reply, &github) == nil && github.Repo != "" && github.Number > 0 {
		return strings.ToLower(fmt.Sprintf("github:%s#%d", github.Repo, github.Number))
	}
	return ""
}

func (s *Service) asksPath(agentID string) (string, error) {
	home, err := s.homeDir(agentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, asksFile), nil
}

// loadAsks returns the asks that have not expired. Callers hold asksMu.
func (s *Service) loadAsks(agentID string) []pendingAsk {
	path, err := s.asksPath(agentID)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("read super agent asks", "super_agent", agentID, "err", err)
		}
		return nil
	}
	var asks []pendingAsk
	if err := json.Unmarshal(data, &asks); err != nil {
		s.log.Warn("decode super agent asks", "super_agent", agentID, "err", err)
		return nil
	}
	now := s.now()
	return slices.DeleteFunc(asks, func(ask pendingAsk) bool { return now.Sub(ask.At) >= askTTL })
}

// saveAsks writes the asks. Callers hold asksMu. Like the Ticket folder, the
// file is a convenience: a failure is only logged.
func (s *Service) saveAsks(agentID string, asks []pendingAsk) {
	path, err := s.asksPath(agentID)
	if err == nil {
		if len(asks) > maxAsks {
			asks = asks[len(asks)-maxAsks:]
		}
		var data []byte
		if data, err = json.Marshal(asks); err == nil {
			err = writeFileAtomic(path, append(data, '\n'))
		}
	}
	if err != nil {
		s.log.Warn("write super agent asks", "super_agent", agentID, "err", err)
	}
}

// notePost records a post. One to another place on behalf of this thread is
// an ask. One back to a thread that was waiting for an answer from this
// event's place settles that ask.
func (s *Service) notePost(agentID, threadKey, sourceID string, reply []byte, target destination, text string) {
	from, to := eventPlace(threadKey), destinationPlace(target.reply)
	if to == "" || to == from || len(reply) == 0 {
		return
	}
	s.asksMu.Lock()
	defer s.asksMu.Unlock()
	asks := s.loadAsks(agentID)
	before := len(asks)
	asks = slices.DeleteFunc(asks, func(ask pendingAsk) bool {
		return ask.Place == from && eventPlace(ask.FromThread) == to
	})
	if len(asks) == before {
		asks = append(asks, pendingAsk{
			At: s.now().UTC(), Place: to, Text: truncateRunes(text, 2000),
			FromThread: threadKey, FromSource: sourceID, FromReply: reply,
		})
	}
	s.saveAsks(agentID, asks)
}

// asksIn lists the newest asks made in a place, for the router.
func (s *Service) asksIn(agentID, threadKey string, names map[string]string) []askView {
	place := eventPlace(threadKey)
	s.asksMu.Lock()
	asks := s.loadAsks(agentID)
	s.asksMu.Unlock()
	now := s.now()
	var out []askView
	for index := len(asks) - 1; index >= 0 && len(out) < maxAsksShown; index-- {
		ask := asks[index]
		if ask.Place != place || ask.FromThread == threadKey {
			continue
		}
		out = append(out, askView{
			Asked: ago(now, ask.At), OnBehalfOf: describeThread(ask.FromThread, "", names),
			To: ask.FromThread, Text: oneLine(ask.Text, 400),
		})
	}
	return out
}

// askedFrom finds the reply address of a thread waiting on an ask.
func (s *Service) askedFrom(agentID, threadKey string) (destination, bool) {
	s.asksMu.Lock()
	asks := s.loadAsks(agentID)
	s.asksMu.Unlock()
	for index := len(asks) - 1; index >= 0; index-- {
		if strings.EqualFold(asks[index].FromThread, threadKey) {
			return destination{sourceID: asks[index].FromSource, reply: asks[index].FromReply}, true
		}
	}
	return destination{}, false
}
