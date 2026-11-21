package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangning19940904/AgentMux/historysearch"
)

const historySearchPath = "/api/v1/history/search"

// historyCmd is how an agent looks up what it was told and what it
// remembers. It calls this machine's AgentMux with the channel conversation
// of the running turn, as amux send does; the daemon decides what that turn
// may see.
func historyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Search an agent's memory and past conversations",
	}
	cmd.AddCommand(historySearchCmd())
	return cmd
}

type historySearchFlags struct {
	channelID, conversationKey, agentID string
	queries                             []string
	around                              string
	limit                               int
	addr, token                         string
	json                                bool
}

func historySearchCmd() *cobra.Command {
	var flags historySearchFlags
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Find memory entries and past turns by keywords, in any language",
		Long: "Search memory and the inputs and answers of past turns by keywords.\n\n" +
			"Give several --query wordings of one question: synonyms, the Chinese and the English words, " +
			"the terms a message would have used. Each wording is searched and the rankings are merged, " +
			"so a match several wordings find ranks first.\n\n" +
			"Results come in two lists: memory (entries and summaries, for an overview) and originals " +
			"(what people sent and what the agent answered, for exact details). --around names a time " +
			"the question is about; it lifts matches from that time and removes nothing.\n\n" +
			"An agent passes the channel_id and conversation_key of its current message; it then sees its " +
			"own history only. Search the originals before saying something was never discussed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := map[string]any{
				"channel_id": flags.channelID, "conversation_key": flags.conversationKey,
				"agent_id": flags.agentID, "queries": flags.queries, "limit": flags.limit,
			}
			if strings.TrimSpace(flags.around) != "" {
				hint, err := parseTimeHint(flags.around, time.Local)
				if err != nil {
					return err
				}
				request["around"] = hint
			}
			result, err := callHistorySearch(cmd.Context(), flags, request)
			if err != nil {
				return err
			}
			if flags.json {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			printHistoryResult(cmd.OutOrStdout(), result)
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.channelID, "channel-id", "", "the channel_id of the message you are answering")
	cmd.Flags().StringVar(&flags.conversationKey, "conversation-key", "", "the conversation_key of the message you are answering")
	cmd.Flags().StringVar(&flags.agentID, "agent", "", "administrators without a running turn: search this agent only")
	cmd.Flags().StringArrayVarP(&flags.queries, "query", "q", nil, "one wording of the question; repeat for synonyms and translations")
	cmd.Flags().StringVar(&flags.around, "around", "", "a time the question names: 2026-11-12, 2026-11-10..2026-11-14 or an RFC 3339 time")
	cmd.Flags().IntVar(&flags.limit, "limit", 0, "results per list (default 8, at most 30)")
	cmd.Flags().StringVar(&flags.addr, "addr", "", "this machine's AgentMux address (default from config)")
	cmd.Flags().StringVar(&flags.token, "token", "", "bridge token (default from config)")
	cmd.Flags().BoolVar(&flags.json, "json", false, "print the answer as JSON")
	return cmd
}

// parseTimeHint reads --around: a day, a range of days, or one instant,
// which stands for the day around it.
func parseTimeHint(value string, location *time.Location) (historysearch.TimeHint, error) {
	value = strings.TrimSpace(value)
	day := func(text string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02", strings.TrimSpace(text), location)
	}
	if from, to, ok := strings.Cut(value, ".."); ok {
		start, err := day(from)
		if err != nil {
			return historysearch.TimeHint{}, fmt.Errorf("--around %q: want YYYY-MM-DD..YYYY-MM-DD", value)
		}
		end, err := day(to)
		if err != nil || end.Before(start) {
			return historysearch.TimeHint{}, fmt.Errorf("--around %q: want YYYY-MM-DD..YYYY-MM-DD, the second day not before the first", value)
		}
		return historysearch.TimeHint{From: start.UTC(), To: end.Add(24*time.Hour - time.Nanosecond).UTC()}, nil
	}
	if start, err := day(value); err == nil {
		return historysearch.TimeHint{From: start.UTC(), To: start.Add(24*time.Hour - time.Nanosecond).UTC()}, nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return historysearch.TimeHint{From: at.Add(-12 * time.Hour).UTC(), To: at.Add(12 * time.Hour).UTC()}, nil
	}
	return historysearch.TimeHint{}, fmt.Errorf("--around %q: want YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD or an RFC 3339 time", value)
}

func callHistorySearch(ctx context.Context, flags historySearchFlags, request map[string]any) (historysearch.Result, error) {
	var result historysearch.Result
	if len(flags.queries) == 0 {
		return result, errors.New("give at least one --query")
	}
	addr, token := flags.addr, flags.token
	if addr == "" || token == "" {
		cfg, _, err := loadConfig(false)
		if err != nil {
			return result, err
		}
		if addr == "" {
			addr = cfg.Server.Addr
		}
		if token == "" {
			token = cfg.Bridge.Token
		}
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	body, _ := json.Marshal(request)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(addr, "/")+historySearchPath, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: time.Minute}).Do(httpRequest)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return result, fmt.Errorf("history search refused (%s): %s", response.Status, failure.Error)
		}
		return result, fmt.Errorf("history search failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("read the answer: %w", err)
	}
	return result, nil
}

func printHistoryResult(out io.Writer, result historysearch.Result) {
	section := func(title string, matches []historysearch.Match) {
		fmt.Fprintf(out, "%s: %d\n", title, len(matches))
		for i, match := range matches {
			var where []string
			where = append(where, strings.TrimPrefix(match.Kind, "turn_"))
			where = append(where, match.Time.Local().Format("2006-01-02 15:04"))
			if match.MemoryScope != "" {
				where = append(where, match.MemoryScope)
			}
			if match.UserName != "" {
				where = append(where, match.UserName)
			}
			if match.ChatID != "" {
				where = append(where, "chat "+match.ChatID)
			}
			if match.InTimeRange {
				where = append(where, "in the time asked about")
			}
			fmt.Fprintf(out, "%d. [%s] %s\n", i+1, strings.Join(where, " · "), match.Snippet)
		}
	}
	section("MEMORY (entries and summaries)", result.Memory)
	fmt.Fprintln(out)
	section("ORIGINALS (past messages and answers)", result.Originals)
	for _, note := range result.Notes {
		fmt.Fprintf(out, "\nnote: %s\n", note)
	}
}
