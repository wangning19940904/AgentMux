package server

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

type apiChannelConversation struct {
	core.Conversation
	ActiveTask  *core.ChannelTask `json:"active_task,omitempty"`
	Queued      int               `json:"queued_tasks"`
	Controller  string            `json:"controller_id,omitempty"`
	ThreadTitle string            `json:"thread_title,omitempty"`
}

func (s *Server) handleChannelConversations(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []apiChannelConversation{})
		return
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID == "" {
		writeErr(w, http.StatusBadRequest, "channel_id is required")
		return
	}
	conversations, err := s.st.ListConversations(r.Context(), "channel:"+channelID, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := s.st.ListChannelTasks(r.Context(), channelID, "", true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	activeByConversation := map[string]*core.ChannelTask{}
	queuedByConversation := map[string]int{}
	queuedControllerByConversation := map[string]string{}
	threadTitles := map[string]string{}
	if s.sessions != nil {
		if threads, listErr := s.sessions.List(r.Context(), "codex", ""); listErr == nil {
			for _, thread := range threads {
				threadTitles[thread.SessionID] = thread.Title
			}
		}
	}
	for i := range tasks {
		task := &tasks[i]
		key := task.ConversationID
		if key == "" {
			key = task.ConversationKey
		}
		if task.Status == core.ChannelTaskQueued {
			queuedByConversation[key]++
			if queuedControllerByConversation[key] == "" {
				queuedControllerByConversation[key] = task.ControllerID
			}
		} else {
			activeByConversation[key] = task
		}
	}
	out := make([]apiChannelConversation, 0, len(conversations))
	for _, conversation := range conversations {
		key := conversation.ID
		active := activeByConversation[key]
		if active == nil {
			active = activeByConversation[conversation.ConversationKey]
		}
		item := apiChannelConversation{
			Conversation: conversation,
			ActiveTask:   active,
			Queued:       queuedByConversation[key] + queuedByConversation[conversation.ConversationKey],
			ThreadTitle:  threadTitles[conversation.NativeSessionID],
		}
		if active != nil {
			item.Controller = active.ControllerID
		} else {
			item.Controller = queuedControllerByConversation[key]
			if item.Controller == "" {
				item.Controller = queuedControllerByConversation[conversation.ConversationKey]
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChannelTasks(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []core.ChannelTask{})
		return
	}
	tasks, err := s.st.ListChannelTasks(r.Context(), strings.TrimSpace(r.URL.Query().Get("channel_id")),
		strings.TrimSpace(r.URL.Query().Get("conversation_id")), false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	positions := map[string]int{}
	for i := len(tasks) - 1; i >= 0; i-- {
		if tasks[i].Status == core.ChannelTaskQueued {
			key := tasks[i].ChannelID + ":" + tasks[i].ConversationKey
			positions[key]++
			tasks[i].QueuePosition = positions[key]
		}
	}
	if s.connect != nil {
		for i := range tasks {
			s.connect.DecorateChannelTask(&tasks[i])
		}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleChannelInteractions(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []core.ChannelInteraction{})
		return
	}
	interactions, err := s.st.ListChannelInteractions(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("channel_id")),
		strings.TrimSpace(r.URL.Query().Get("conversation_id")),
		r.URL.Query().Get("pending") != "false")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, interactions)
}

func (s *Server) handleChannelInteractionRespond(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "interaction responses are accepted only from the local console")
		return
	}
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	action, ok := decodeJSON[core.AgentInteractionAction](w, r)
	if !ok {
		return
	}
	if err := s.connect.ResolveChannelInteractionLocal(r.Context(), action); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleChannelConversationBind(w http.ResponseWriter, r *http.Request) {
	if s.st == nil || s.connect == nil || s.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, "channel/session runtime is unavailable")
		return
	}
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		ThreadID       string `json:"thread_id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	conversation, err := s.channelConversation(r, req.ChannelID, req.ConversationID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.sessions.List(r.Context(), "codex", "desktop")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list Codex threads: "+err.Error())
		return
	}
	var found bool
	for _, item := range items {
		if item.SessionID != strings.TrimSpace(req.ThreadID) {
			continue
		}
		found = true
		if !sameCanonicalPath(item.ProjectDir, conversation.WorkDir) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": fmt.Sprintf("thread belongs to %q, conversation uses %q", item.ProjectDir, conversation.WorkDir),
			})
			return
		}
		break
	}
	if !found {
		writeErr(w, http.StatusNotFound, "Codex thread was not found")
		return
	}
	if err := s.connect.BindChannelConversation(r.Context(), req.ChannelID, req.ConversationID, req.ThreadID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "thread_id": strings.TrimSpace(req.ThreadID)})
}

func (s *Server) handleChannelConversationOpen(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, "session service is unavailable")
		return
	}
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		ThreadID       string `json:"thread_id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		conversation, err := s.channelConversation(r, req.ChannelID, req.ConversationID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		threadID = conversation.NativeSessionID
	}
	result, err := s.sessions.OpenCodexThread(r.Context(), threadID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) channelConversation(r *http.Request, channelID, conversationID string) (*core.Conversation, error) {
	channelID = strings.TrimSpace(channelID)
	conversationID = strings.TrimSpace(conversationID)
	if channelID == "" || conversationID == "" {
		return nil, fmt.Errorf("channel_id and conversation_id are required")
	}
	conversations, err := s.st.ListConversations(r.Context(), "channel:"+channelID, false)
	if err != nil {
		return nil, err
	}
	for i := range conversations {
		if conversations[i].ID == conversationID {
			return &conversations[i], nil
		}
	}
	return nil, fmt.Errorf("conversation was not found in channel")
}

func sameCanonicalPath(left, right string) bool {
	canonical := func(value string) string {
		if value == "" {
			return ""
		}
		absolute, err := filepath.Abs(value)
		if err == nil {
			value = absolute
		}
		value = filepath.Clean(value)
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			value = resolved
		}
		return value
	}
	return canonical(left) != "" && canonical(left) == canonical(right)
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
