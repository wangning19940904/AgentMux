package provider

import (
	"fmt"
	"strings"
)

// xlate.go is the star-shaped protocol translation hub. Every client entry
// (Anthropic Messages, OpenAI Chat, OpenAI Responses, Gemini generateContent)
// is decoded into a single intermediate representation (IR) shaped like an
// OpenAI Chat Completions body, and every upstream provider protocol is
// encoded from that IR. This mirrors ccr-x's "OpenAI Chat as lingua franca"
// design: N client protocols + N upstream protocols need only 2N converters
// instead of N*N. When the client and upstream speak the same protocol the
// proxy skips the IR entirely and passes the body through untouched.
//
// IR request  = map[string]any in OpenAI Chat Completions request shape.
// IR response = map[string]any in OpenAI Chat Completions response shape.

// Wire protocol identifiers used across the translation layer.
const (
	protoAnthropic  = "anthropic"
	protoOpenAIChat = "openai_chat"
	protoResponses  = "openai_responses"
	protoGemini     = "gemini"
)

// normalizeProto maps the various API-format/wire aliases onto a canonical
// protocol id. An empty string stays empty so callers can apply per-entry
// defaults.
func normalizeProto(format string) string {
	switch strings.TrimSpace(strings.ToLower(format)) {
	case "anthropic", "claude", "messages":
		return protoAnthropic
	case "openai_chat", "chat", "chat_completions":
		return protoOpenAIChat
	case "openai_responses", "responses":
		return protoResponses
	case "gemini", "gemini_native", "generatecontent":
		return protoGemini
	default:
		return ""
	}
}

// upstreamProto resolves the provider's upstream protocol, defaulting to the
// given fallback when the provider declares nothing.
func upstreamProto(apiFormat, fallback string) string {
	if p := normalizeProto(apiFormat); p != "" {
		return p
	}
	return fallback
}

// decodeToIR converts a client request body (in clientProto) into the OpenAI
// Chat IR. The IR keeps the client's original model id; the forwarder rewrites
// it to the upstream model before encoding.
func decodeToIR(clientProto string, body map[string]any) (map[string]any, error) {
	switch clientProto {
	case protoOpenAIChat:
		return body, nil
	case protoAnthropic:
		return anthropicToChatRequest(body, "")
	case protoResponses:
		return responsesToChatRequest(body)
	case protoGemini:
		return geminiToChatRequest(body)
	default:
		return nil, fmt.Errorf("unsupported client protocol %q", clientProto)
	}
}

// encodeFromIR converts the OpenAI Chat IR request into an upstream request
// body for upstreamProto.
func encodeFromIR(upstream string, ir map[string]any) (map[string]any, error) {
	switch upstream {
	case protoOpenAIChat:
		return ir, nil
	case protoAnthropic:
		return chatToAnthropicRequest(ir), nil
	case protoResponses:
		return chatToResponsesRequest(ir), nil
	case protoGemini:
		return chatToGeminiRequest(ir), nil
	default:
		return nil, fmt.Errorf("unsupported upstream protocol %q", upstream)
	}
}

// upstreamRespToIR converts a non-streaming upstream response body (in
// upstreamProto) into the OpenAI Chat IR response shape.
func upstreamRespToIR(upstream string, body map[string]any) (map[string]any, error) {
	switch upstream {
	case protoOpenAIChat:
		return body, nil
	case protoAnthropic:
		return anthropicRespToChat(body), nil
	case protoResponses:
		return responsesRespToChat(body), nil
	case protoGemini:
		return geminiRespToChat(body), nil
	default:
		return nil, fmt.Errorf("unsupported upstream protocol %q", upstream)
	}
}

// irRespToClient converts an OpenAI Chat IR response into the client's wire
// body (in clientProto). requestModel is echoed back when the IR omits it.
func irRespToClient(clientProto string, ir map[string]any, requestModel string) (map[string]any, error) {
	switch clientProto {
	case protoOpenAIChat:
		return ir, nil
	case protoAnthropic:
		return chatToAnthropicResponse(ir, requestModel), nil
	case protoResponses:
		return chatToResponsesResponse(ir, requestModel), nil
	case protoGemini:
		return chatToGeminiResponse(ir, requestModel), nil
	default:
		return nil, fmt.Errorf("unsupported client protocol %q", clientProto)
	}
}
