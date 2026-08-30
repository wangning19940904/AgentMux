package server

import (
	"context"
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
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, err = s.enrichSessionRows(r.Context(), items, providerID, surface)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleCodexDesktopThreads(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, []sessionstore.Meta{})
		return
	}
	items, err := s.sessions.List(r.Context(), "codex", "desktop")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list Codex Desktop threads: "+err.Error())
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
			writeErr(w, http.StatusNotFound, err.Error())
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
		if terminal, ok := s.sender.(core.ConversationTerminalController); ok {
			info, terminalErr := terminal.TerminalSessionInfo(r.Context(), strings.TrimPrefix(conversation.Scope, "channel:"), *conversation)
			if terminalErr == nil && info.Backend != "" {
				snapshot, snapshotErr := terminal.TerminalSnapshot(r.Context(), strings.TrimPrefix(conversation.Scope, "channel:"), *conversation)
				if snapshotErr != nil {
					writeErr(w, http.StatusConflict, snapshotErr.Error())
					return
				}
				writeJSON(w, http.StatusOK, []sessionstore.Message{{Role: "assistant", Kind: "terminal", Content: snapshot}})
				return
			}
		}
	}
	items, err := s.sessions.Messages(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.Text = strings.TrimSpace(req.Text)
	if req.ChannelID == "" || req.ConversationID == "" || req.Text == "" {
		writeErr(w, http.StatusBadRequest, "channel_id, conversation_id and text are required")
		return
	}
	sender, ok := s.sender.(core.ConversationSender)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "conversation chat is unavailable")
		return
	}
	answer, err := sender.SendToConversation(r.Context(), req.ChannelID, req.ConversationID, req.Text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answer": answer})
}

func (s *Server) handleSessionResume(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, "sessions not enabled")
		return
	}
	req, ok := decodeJSON[sessionstore.ResumeRequest](w, r)
	if !ok {
		return
	}
	res, err := s.sessions.Resume(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "sessions can be stopped only from the local console")
		return
	}
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "conversation store is unavailable")
		return
	}
	controller, ok := s.sender.(core.ConversationRuntimeController)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "conversation control is unavailable")
		return
	}
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		ActiveTaskID   string `json:"active_task_id,omitempty"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	conversation, err := s.channelConversation(r, req.ChannelID, req.ConversationID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := controller.StopConversation(r.Context(), strings.TrimSpace(req.ChannelID), conversation.ConversationKey, req.ActiveTaskID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": state.Status, "can_stop": state.CanStop, "task_id": state.TaskID,
	})
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, "sessions not enabled")
		return
	}
	req := sessionstore.ResumeRequest{
		ProviderID: r.URL.Query().Get("provider"),
		Surface:    r.URL.Query().Get("surface"),
		SessionID:  r.URL.Query().Get("session_id"),
		SourcePath: r.URL.Query().Get("source_path"),
	}
	if err := s.sessions.Delete(r.Context(), req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
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
		if native[i].RunStatus == "" {
			native[i].RunStatus = core.ConversationStatusIdle
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
	tasks, err := s.st.ListLatestChannelTasks(ctx)
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
	latestTaskByConversation := make(map[string]core.ChannelTask, len(tasks))
	latestTaskByKey := make(map[string]core.ChannelTask, len(tasks))
	for _, task := range tasks {
		if task.ConversationID != "" {
			if _, exists := latestTaskByConversation[task.ConversationID]; !exists {
				latestTaskByConversation[task.ConversationID] = task
			}
		}
		key := sessionConversationTaskKey(task.ChannelID, task.ConversationKey)
		if _, exists := latestTaskByKey[key]; !exists {
			latestTaskByKey[key] = task
		}
	}
	runtimeController, runtimeControlAvailable := s.sender.(core.ConversationRuntimeController)
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
		row.RunStatus = core.ConversationStatusIdle
		latestTask, taskFound := latestTaskByConversation[conversation.ID]
		if !taskFound {
			latestTask, taskFound = latestTaskByKey[sessionConversationTaskKey(channelID, conversation.ConversationKey)]
		}
		if taskFound {
			row.RunStatus = string(latestTask.Status)
			if sessionRunStatusActive(row.RunStatus) {
				row.ActiveTaskID = latestTask.ID
			}
		}
		if runtimeControlAvailable {
			state, stateErr := runtimeController.ConversationRuntimeState(ctx, channelID, conversation.ConversationKey)
			if stateErr == nil {
				if sessionRunStatusActive(state.Status) || sessionRunStatusActive(row.RunStatus) ||
					row.RunStatus == "" || row.RunStatus == core.ConversationStatusIdle {
					row.RunStatus = state.Status
				}
				row.CanStop = state.CanStop
				if state.TaskID != "" {
					row.ActiveTaskID = state.TaskID
				}
			}
		}
		if agent.SessionBackend == "tmux" {
			row.TerminalBackend = "tmux"
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

func (s *Server) handleSessionTerminalGet(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "terminal access is available only from the local console")
		return
	}
	conversation, err := s.channelConversation(r, r.URL.Query().Get("channel_id"), r.URL.Query().Get("conversation_id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	controller, ok := s.sender.(core.ConversationTerminalController)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "terminal control is unavailable")
		return
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	info, err := controller.TerminalSessionInfo(r.Context(), channelID, *conversation)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if info.Backend == "" {
		writeErr(w, http.StatusNotFound, "conversation has no managed terminal session")
		return
	}
	snapshot, err := controller.TerminalSnapshot(r.Context(), channelID, *conversation)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"info": info, "snapshot": snapshot})
}

func (s *Server) handleSessionTerminalInput(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "terminal input is available only from the local console")
		return
	}
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		Text           string `json:"text"`
		Submit         bool   `json:"submit"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	conversation, err := s.channelConversation(r, req.ChannelID, req.ConversationID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	controller, ok := s.sender.(core.ConversationTerminalController)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "terminal control is unavailable")
		return
	}
	if err := controller.WriteTerminal(r.Context(), strings.TrimSpace(req.ChannelID), *conversation, req.Text, req.Submit); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleSessionTerminalResize(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "terminal resize is available only from the local console")
		return
	}
	var req struct {
		ChannelID      string `json:"channel_id"`
		ConversationID string `json:"conversation_id"`
		Columns        int    `json:"columns"`
		Rows           int    `json:"rows"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	conversation, err := s.channelConversation(r, req.ChannelID, req.ConversationID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	controller, ok := s.sender.(core.ConversationTerminalController)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "terminal control is unavailable")
		return
	}
	if err := controller.ResizeTerminal(r.Context(), strings.TrimSpace(req.ChannelID), *conversation, req.Columns, req.Rows); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeOK(w)
}

func sessionConversationTaskKey(channelID, conversationKey string) string {
	return strings.TrimSpace(channelID) + "\x00" + strings.TrimSpace(conversationKey)
}

func sessionRunStatusActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(core.ChannelTaskQueued), string(core.ChannelTaskRunning), string(core.ChannelTaskWaitingInput), core.ConversationStatusStopping:
		return true
	default:
		return false
	}
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
