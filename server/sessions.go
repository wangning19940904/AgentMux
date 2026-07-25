package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	sessionstore "github.com/wangning19940904/AgentMux/sessions"
)

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	providerID := strings.TrimSpace(r.URL.Query().Get("provider"))
	surface := strings.TrimSpace(r.URL.Query().Get("surface"))
	items, err := s.sessions.List(r.Context(), providerID, surface)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items, err = s.enrichSessionRows(r.Context(), items, providerID, surface)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	req := sessionstore.ResumeRequest{
		ProviderID: r.URL.Query().Get("provider"),
		Surface:    r.URL.Query().Get("surface"),
		SessionID:  r.URL.Query().Get("session_id"),
		SourcePath: r.URL.Query().Get("source_path"),
		ProjectDir: r.URL.Query().Get("project_dir"),
	}
	if conversationID := strings.TrimSpace(r.URL.Query().Get("conversation_id")); conversationID != "" {
		conversation, providerID, err := s.resolveConversationSession(r.Context(), conversationID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		if conversation.NativeSessionID == "" {
			writeJSON(w, http.StatusOK, []sessionstore.Message{})
			return
		}
		req.SessionID = conversation.NativeSessionID
		req.ProjectDir = conversation.WorkDir
		req.SourcePath = ""
		req.Surface = ""
		if providerID != "" {
			req.ProviderID = providerID
		}
	}
	items, err := s.sessions.Messages(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSessionMessageSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		Text           string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.Text = strings.TrimSpace(req.Text)
	if req.ChannelID == "" || req.ConversationID == "" || req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel_id, conversation_id and text are required"})
		return
	}
	sender, ok := s.sender.(core.ConversationSender)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation chat is unavailable"})
		return
	}
	answer, err := sender.SendToConversation(r.Context(), req.ChannelID, req.ConversationID, req.Text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answer": answer})
}

func (s *Server) handleSessionResume(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions not enabled"})
		return
	}
	var req sessionstore.ResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.sessions.Resume(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions not enabled"})
		return
	}
	req := sessionstore.ResumeRequest{
		ProviderID: r.URL.Query().Get("provider"),
		Surface:    r.URL.Query().Get("surface"),
		SessionID:  r.URL.Query().Get("session_id"),
		SourcePath: r.URL.Query().Get("source_path"),
	}
	if err := s.sessions.Delete(r.Context(), req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) enrichSessionRows(
	ctx context.Context,
	native []sessionstore.Meta,
	providerFilter string,
	surfaceFilter string,
) ([]sessionstore.Meta, error) {
	for i := range native {
		if native[i].Origin == "" {
			native[i].Origin = "local"
		}
		if native[i].NativeSessionID == "" {
			native[i].NativeSessionID = native[i].SessionID
		}
	}
	if s.st == nil {
		return native, nil
	}
	conversations, err := s.st.ListConversations(ctx, "", false)
	if err != nil {
		return nil, err
	}
	channels, err := s.st.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	agents, err := s.st.ListAgentInstances(ctx)
	if err != nil {
		return nil, err
	}
	channelByID := make(map[string]core.Channel, len(channels))
	for _, channel := range channels {
		channelByID[channel.ID] = channel
	}
	agentByID := make(map[string]core.AgentInstance, len(agents))
	for _, agent := range agents {
		agentByID[agent.ID] = agent
	}
	usedNative := make(map[int]bool, len(native))
	rows := make([]sessionstore.Meta, 0, len(native)+len(conversations))
	for _, conversation := range conversations {
		if !strings.HasPrefix(conversation.Scope, "channel:") {
			continue
		}
		channelID := strings.TrimPrefix(conversation.Scope, "channel:")
		channel, channelFound := channelByID[channelID]
		agentID := conversation.AgentID
		if agentID == "" && channelFound {
			agentID = channel.AgentID
		}
		agent := agentByID[agentID]
		providerID := sessionProviderForRuntime(agent.RuntimeID)

		matchIndex := findNativeSession(native, conversation.NativeSessionID, providerID, conversation.WorkDir)
		var row sessionstore.Meta
		if matchIndex >= 0 {
			row = native[matchIndex]
		} else {
			if surfaceFilter != "" {
				continue
			}
			row = sessionstore.Meta{
				ProviderID: providerID,
				Surface:    "channel",
				SessionID:  conversation.ID,
				Available:  true,
			}
		}
		if row.ProviderID == "" {
			row.ProviderID = firstSessionNonEmpty(providerID, agent.RuntimeID, "agent")
		}
		if providerFilter != "" && !sameSessionProvider(providerFilter, row.ProviderID) {
			continue
		}
		if matchIndex >= 0 {
			usedNative[matchIndex] = true
		}
		if row.SessionID == "" {
			row.SessionID = firstSessionNonEmpty(conversation.NativeSessionID, conversation.ID)
		}
		row.NativeSessionID = conversation.NativeSessionID
		row.Origin = "channel"
		row.AgentID = agentID
		row.AgentName = firstSessionNonEmpty(agent.Name, agentID)
		row.ChannelID = channelID
		row.ChannelName = firstSessionNonEmpty(channel.Name, channelID)
		row.ChannelType = channel.Type
		row.ConversationID = conversation.ID
		row.ConversationKey = conversation.ConversationKey
		row.ChatID = conversation.ChatID
		row.ChatType = conversation.ChatType
		row.CanChat = channelFound && channel.Enabled
		row.ProjectDir = firstSessionNonEmpty(conversation.WorkDir, row.ProjectDir)
		row.Title = firstSessionNonEmpty(conversation.Title, row.Title, conversation.ConversationKey, conversation.ChatID)
		if row.CreatedAt.IsZero() {
			row.CreatedAt = conversation.CreatedAt
		}
		if conversation.LastActiveAt.After(row.LastActiveAt) {
			row.LastActiveAt = conversation.LastActiveAt
		}
		if conversation.MessageCount > row.MessageCount {
			row.MessageCount = conversation.MessageCount
		}
		rows = append(rows, row)
	}
	for i, item := range native {
		if !usedNative[i] {
			rows = append(rows, item)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].LastActiveAt.After(rows[j].LastActiveAt)
	})
	return rows, nil
}

func (s *Server) resolveConversationSession(ctx context.Context, conversationID string) (*core.Conversation, string, error) {
	if s.st == nil {
		return nil, "", fmt.Errorf("conversation store is unavailable")
	}
	conversations, err := s.st.ListConversations(ctx, "", false)
	if err != nil {
		return nil, "", err
	}
	var found *core.Conversation
	for i := range conversations {
		if conversations[i].ID == conversationID {
			found = &conversations[i]
			break
		}
	}
	if found == nil {
		return nil, "", fmt.Errorf("conversation %q was not found", conversationID)
	}
	agentID := found.AgentID
	if agentID == "" && strings.HasPrefix(found.Scope, "channel:") {
		channel, getErr := s.st.GetChannel(ctx, strings.TrimPrefix(found.Scope, "channel:"))
		if getErr != nil {
			return nil, "", getErr
		}
		if channel != nil {
			agentID = channel.AgentID
		}
	}
	if agentID == "" {
		return found, "", nil
	}
	agent, err := s.st.GetAgentInstance(ctx, agentID)
	if err != nil {
		return nil, "", err
	}
	if agent == nil {
		return found, "", nil
	}
	return found, sessionProviderForRuntime(agent.RuntimeID), nil
}

func findNativeSession(items []sessionstore.Meta, sessionID, providerID, workDir string) int {
	if sessionID == "" {
		return -1
	}
	fallback := -1
	for i, item := range items {
		if item.SessionID != sessionID {
			continue
		}
		if fallback < 0 {
			fallback = i
		}
		if providerID != "" && !sameSessionProvider(providerID, item.ProviderID) {
			continue
		}
		if workDir == "" || item.ProjectDir == "" || sameCanonicalPath(workDir, item.ProjectDir) {
			return i
		}
	}
	return fallback
}

func sessionProviderForRuntime(runtimeID string) string {
	runtimeID = strings.ToLower(strings.TrimSpace(runtimeID))
	switch {
	case strings.Contains(runtimeID, "claude"):
		return "claudecode"
	case strings.Contains(runtimeID, "codex"):
		return "codex"
	default:
		return runtimeID
	}
}

func sameSessionProvider(left, right string) bool {
	left = sessionProviderForRuntime(left)
	right = sessionProviderForRuntime(right)
	return left == right
}

func firstSessionNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
