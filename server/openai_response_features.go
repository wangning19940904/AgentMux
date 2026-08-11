package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

const maxOpenAIResponseRequestBytes = 32 << 20

type openAIParsedInput struct {
	Text        string
	Attachments []core.AgentAttachment
}

type openAITool struct {
	Type        string
	Name        string
	Description string
	Parameters  map[string]any
	Strict      bool
	Raw         map[string]any
}

type openAIFunctionCall struct {
	ID        string
	CallID    string
	Name      string
	Arguments string
}

type openAIFinalOutput struct {
	Text          string
	FunctionCalls []openAIFunctionCall
}

func parseOpenAIInput(raw json.RawMessage) (openAIParsedInput, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return openAIParsedInput{}, errors.New("field is required")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return openAIParsedInput{}, errors.New("must not be empty")
		}
		return openAIParsedInput{Text: text}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return openAIParsedInput{}, errors.New("must be a string or an array of input items")
	}
	if len(items) == 0 {
		return openAIParsedInput{}, errors.New("must not be empty")
	}
	var messages []string
	var attachments []core.AgentAttachment
	for _, rawItem := range items {
		var item struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return openAIParsedInput{}, errors.New("contains an invalid input item")
		}
		switch item.Type {
		case "", "message":
			parsed, err := parseOpenAIMessageContent(item.Content)
			if err != nil {
				return openAIParsedInput{}, err
			}
			attachments = append(attachments, parsed.Attachments...)
			role := strings.TrimSpace(item.Role)
			if role == "" {
				role = "user"
			}
			if parsed.Text != "" {
				messages = append(messages, strings.ToUpper(role)+":\n"+parsed.Text)
			}
		case "function_call_output":
			output, err := openAIRawText(item.Output)
			if err != nil {
				return openAIParsedInput{}, fmt.Errorf("function_call_output %q: %w", item.CallID, err)
			}
			messages = append(messages, "FUNCTION RESULT ("+strings.TrimSpace(item.CallID)+"):\n"+output)
		case "function_call":
			arguments, err := openAIRawText(item.Arguments)
			if err != nil {
				return openAIParsedInput{}, fmt.Errorf("function_call %q: %w", item.Name, err)
			}
			messages = append(messages, "ASSISTANT FUNCTION CALL "+item.Name+" ("+item.CallID+"):\n"+arguments)
		default:
			if item.Type == "input_image" || item.Type == "input_file" || item.Type == "input_text" {
				parsed, err := parseOpenAIMessageContent(json.RawMessage("[" + string(rawItem) + "]"))
				if err != nil {
					return openAIParsedInput{}, err
				}
				if parsed.Text != "" {
					messages = append(messages, "USER:\n"+parsed.Text)
				}
				attachments = append(attachments, parsed.Attachments...)
				continue
			}
			if strings.HasSuffix(item.Type, "_call_output") || strings.HasSuffix(item.Type, "_tool_call_output") {
				output, err := openAIRawText(item.Output)
				if err != nil {
					return openAIParsedInput{}, fmt.Errorf("%s %q: %w", item.Type, item.CallID, err)
				}
				messages = append(messages, strings.ToUpper(item.Type)+" ("+item.CallID+"):\n"+output)
				continue
			}
			return openAIParsedInput{}, fmt.Errorf("input item type %q is not supported", item.Type)
		}
	}
	if len(messages) == 0 && len(attachments) == 0 {
		return openAIParsedInput{}, errors.New("must contain text, an image, a file, or a function result")
	}
	prompt := strings.Join(messages, "\n\n")
	if len(messages) == 1 && strings.HasPrefix(prompt, "USER:\n") {
		prompt = strings.TrimPrefix(prompt, "USER:\n")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Analyze the attached input."
	}
	return openAIParsedInput{Text: prompt, Attachments: attachments}, nil
}

func parseOpenAIMessageContent(raw json.RawMessage) (openAIParsedInput, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return openAIParsedInput{}, errors.New("message content is required")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return openAIParsedInput{}, errors.New("message content must not be empty")
		}
		return openAIParsedInput{Text: text}, nil
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL string          `json:"image_url"`
		FileURL  string          `json:"file_url"`
		FileData string          `json:"file_data"`
		FileID   string          `json:"file_id"`
		Filename string          `json:"filename"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return openAIParsedInput{}, errors.New("message content must be text or an array of content parts")
	}
	var texts []string
	var attachments []core.AgentAttachment
	for index, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case "input_image":
			attachment, err := openAIImageAttachment(part.ImageURL, part.FileID, index)
			if err != nil {
				return openAIParsedInput{}, err
			}
			attachments = append(attachments, attachment)
		case "input_file":
			attachment, err := openAIFileAttachment(part.FileData, part.FileURL, part.FileID, part.Filename, index)
			if err != nil {
				return openAIParsedInput{}, err
			}
			attachments = append(attachments, attachment)
		default:
			return openAIParsedInput{}, fmt.Errorf("content type %q is not supported", part.Type)
		}
	}
	if len(texts) == 0 && len(attachments) == 0 {
		return openAIParsedInput{}, errors.New("message content must not be empty")
	}
	return openAIParsedInput{Text: strings.Join(texts, "\n"), Attachments: attachments}, nil
}

func openAIImageAttachment(value, fileID string, index int) (core.AgentAttachment, error) {
	value = strings.TrimSpace(value)
	fileID = strings.TrimSpace(fileID)
	if (value == "") == (fileID == "") {
		return core.AgentAttachment{}, errors.New("input_image requires exactly one of image_url or file_id")
	}
	if fileID != "" {
		return core.AgentAttachment{Kind: "image", Name: fmt.Sprintf("image-%d", index+1), URL: "openai-file-id:" + fileID}, nil
	}
	if strings.HasPrefix(value, "data:") {
		mimeType, data, err := decodeOpenAIDataURL(value)
		if err != nil {
			return core.AgentAttachment{}, fmt.Errorf("input_image.image_url: %w", err)
		}
		return core.AgentAttachment{Kind: "image", Name: fmt.Sprintf("image-%d%s", index+1, extensionForMIME(mimeType)), MIMEType: mimeType, Data: data}, nil
	}
	if err := validateOpenAIURL(value); err != nil {
		return core.AgentAttachment{}, fmt.Errorf("input_image.image_url: %w", err)
	}
	return core.AgentAttachment{Kind: "image", Name: fmt.Sprintf("image-%d", index+1), URL: value}, nil
}

func openAIFileAttachment(fileData, fileURL, fileID, filename string, index int) (core.AgentAttachment, error) {
	provided := 0
	for _, value := range []string{fileData, fileURL, fileID} {
		if strings.TrimSpace(value) != "" {
			provided++
		}
	}
	if provided != 1 {
		return core.AgentAttachment{}, errors.New("input_file requires exactly one of file_data, file_url, or file_id")
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = fmt.Sprintf("file-%d", index+1)
	}
	if fileData != "" {
		mimeType := "application/octet-stream"
		var data []byte
		var err error
		if strings.HasPrefix(strings.TrimSpace(fileData), "data:") {
			mimeType, data, err = decodeOpenAIDataURL(strings.TrimSpace(fileData))
		} else {
			data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(fileData))
		}
		if err != nil {
			return core.AgentAttachment{}, fmt.Errorf("input_file.file_data: invalid base64 data: %w", err)
		}
		return core.AgentAttachment{Kind: "file", Name: name, MIMEType: mimeType, Data: data}, nil
	}
	if fileURL != "" {
		if err := validateOpenAIURL(fileURL); err != nil {
			return core.AgentAttachment{}, fmt.Errorf("input_file.file_url: %w", err)
		}
		return core.AgentAttachment{Kind: "file", Name: name, URL: fileURL}, nil
	}
	// File IDs are accepted as opaque references so clients can preserve the
	// Responses wire format. Agents with a configured file connector can resolve
	// them; otherwise the prompt exposes the ID explicitly.
	return core.AgentAttachment{Kind: "file", Name: name, URL: "openai-file-id:" + strings.TrimSpace(fileID)}, nil
}

func decodeOpenAIDataURL(value string) (string, []byte, error) {
	header, payload, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") {
		return "", nil, errors.New("invalid data URL")
	}
	if !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", nil, errors.New("data URL must use base64 encoding")
	}
	mimeType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, err
	}
	if len(data) > 25<<20 {
		return "", nil, errors.New("attachment exceeds 25 MiB")
	}
	return mimeType, data, nil
}

func validateOpenAIURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("must be an absolute http(s) URL")
	}
	return nil
}

func extensionForMIME(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func openAIRawText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", errors.New("value is required")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func parseOpenAITools(rawTools []json.RawMessage) ([]openAITool, error) {
	tools := make([]openAITool, 0, len(rawTools))
	for index, raw := range rawTools {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("item %d is invalid: %w", index, err)
		}
		toolType, _ := object["type"].(string)
		toolType = strings.TrimSpace(toolType)
		if toolType == "" {
			return nil, fmt.Errorf("item %d is missing type", index)
		}
		tool := openAITool{Type: toolType, Raw: object}
		if toolType == "function" {
			tool.Name, _ = object["name"].(string)
			tool.Description, _ = object["description"].(string)
			tool.Parameters, _ = object["parameters"].(map[string]any)
			tool.Strict, _ = object["strict"].(bool)
			if strings.TrimSpace(tool.Name) == "" {
				return nil, fmt.Errorf("function tool %d is missing name", index)
			}
			if tool.Parameters == nil {
				tool.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func parseOpenAIOutputFormat(text *openAIResponseTextInput) (openAIOutputFormat, error) {
	format := openAIOutputFormat{Type: "text"}
	if text == nil || text.Format == nil {
		return format, nil
	}
	format = *text.Format
	format.Type = strings.TrimSpace(format.Type)
	if format.Type == "" {
		format.Type = "text"
	}
	switch format.Type {
	case "text", "json_object":
		return format, nil
	case "json_schema":
		if strings.TrimSpace(format.Name) == "" {
			return format, errors.New("json_schema requires name")
		}
		if len(format.Schema) == 0 {
			return format, errors.New("json_schema requires schema")
		}
		return format, nil
	default:
		return format, fmt.Errorf("unsupported format type %q", format.Type)
	}
}

func appendOpenAIToolInstructions(prompt string, req openAIResponseRequest) string {
	if len(req.parsedTools) == 0 {
		return prompt
	}
	var functionDefs []map[string]any
	var hosted []map[string]any
	for _, tool := range req.parsedTools {
		if tool.Type == "function" {
			functionDefs = append(functionDefs, map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
			})
		} else {
			hosted = append(hosted, tool.Raw)
		}
	}
	var instructions []string
	if len(hosted) > 0 {
		encoded, _ := json.Marshal(hosted)
		instructions = append(instructions, "The caller requested these hosted/runtime tool configurations: "+string(encoded)+". Use the equivalent tools configured on this Agent when helpful. Complete hosted tool work yourself before answering.")
		if openAIToolChoiceRequired(req.ToolChoice) && len(functionDefs) == 0 {
			instructions = append(instructions, "Use at least one of the requested hosted/runtime tools before answering.")
		}
	}
	if len(functionDefs) > 0 && !openAIToolChoiceIsNone(req.ToolChoice) {
		encoded, _ := json.Marshal(functionDefs)
		instructions = append(instructions, "The caller provides these functions (the caller, not the Agent, executes them): "+string(encoded)+". If a function is needed, return only JSON in this exact envelope: {\"agentmux_function_calls\":[{\"name\":\"function_name\",\"arguments\":{}}]}. Do not execute caller functions yourself and do not add prose around the envelope.")
		if openAIToolChoiceRequired(req.ToolChoice) {
			instructions = append(instructions, "At least one caller function call is required for this response.")
		}
		if name := openAIToolChoiceFunctionName(req.ToolChoice); name != "" {
			instructions = append(instructions, "Call the specific caller function named "+name+".")
		}
	}
	if len(instructions) == 0 {
		return prompt
	}
	return prompt + "\n\nTool instructions:\n" + strings.Join(instructions, "\n")
}

func appendOpenAIFormatInstructions(prompt string, format openAIOutputFormat) string {
	switch format.Type {
	case "json_object":
		return prompt + "\n\nOutput requirement: Return only one valid JSON object, without Markdown fences or commentary."
	case "json_schema":
		encoded, _ := json.Marshal(format.Schema)
		return prompt + "\n\nOutput requirement: Return only valid JSON matching this JSON Schema, without Markdown fences or commentary:\n" + string(encoded)
	default:
		return prompt
	}
}

func openAIToolChoiceIsNone(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && value == "none"
}

func openAIToolChoiceRequired(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value == "required"
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	return object["type"] == "function" || object["type"] == "allowed_tools"
}

func openAIToolChoiceFunctionName(raw json.RawMessage) string {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil || object["type"] != "function" {
		return ""
	}
	name, _ := object["name"].(string)
	return strings.TrimSpace(name)
}

func hasOpenAIFunctionTools(req openAIResponseRequest) bool {
	if openAIToolChoiceIsNone(req.ToolChoice) {
		return false
	}
	for _, tool := range req.parsedTools {
		if tool.Type == "function" {
			return true
		}
	}
	return false
}

func openAIFunctionEnvelopeSchema(req openAIResponseRequest) map[string]any {
	variants := make([]any, 0, len(req.parsedTools))
	for _, tool := range req.parsedTools {
		if tool.Type != "function" {
			continue
		}
		variants = append(variants, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "const": tool.Name},
				"arguments": tool.Parameters,
			},
			"required": []any{"name", "arguments"},
		})
	}
	calls := map[string]any{
		"type": "array", "minItems": 1,
		"items": map[string]any{"anyOf": variants},
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
		calls["maxItems"] = 1
	}
	if req.MaxToolCalls != nil {
		calls["maxItems"] = *req.MaxToolCalls
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"agentmux_function_calls": calls,
		},
		"required": []any{"agentmux_function_calls"},
	}
}

func prepareOpenAIFinalOutput(req *openAIResponseRequest, result *core.InvocationResult) error {
	if req == nil || result == nil {
		return nil
	}
	final := &openAIFinalOutput{Text: result.Answer}
	if hasOpenAIFunctionTools(*req) {
		calls, matched, err := parseOpenAIFunctionCalls(result.Answer, *req)
		if err != nil {
			return err
		}
		if matched {
			final.Text = ""
			final.FunctionCalls = calls
		} else if openAIToolChoiceRequired(req.ToolChoice) {
			return errors.New("the Agent did not return a function call required by tool_choice")
		}
	}
	if len(final.FunctionCalls) == 0 {
		normalized, err := normalizeOpenAIStructuredOutput(final.Text, req.outputFormat)
		if err != nil {
			return err
		}
		final.Text = normalized
		result.Answer = normalized
	}
	req.finalOutput = final
	return nil
}

func parseOpenAIFunctionCalls(answer string, req openAIResponseRequest) ([]openAIFunctionCall, bool, error) {
	raw, err := extractOpenAIJSON(answer)
	if err != nil {
		return nil, false, nil
	}
	var envelope struct {
		Calls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"agentmux_function_calls"`
		ToolCalls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false, nil
	}
	entries := envelope.Calls
	if len(entries) == 0 {
		entries = envelope.ToolCalls
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	allowed := map[string]openAITool{}
	for _, tool := range req.parsedTools {
		if tool.Type == "function" {
			allowed[tool.Name] = tool
		}
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && len(entries) > 1 {
		return nil, true, errors.New("Agent returned multiple calls while parallel_tool_calls=false")
	}
	if req.MaxToolCalls != nil && int64(len(entries)) > *req.MaxToolCalls {
		return nil, true, fmt.Errorf("Agent returned %d calls, exceeding max_tool_calls=%d", len(entries), *req.MaxToolCalls)
	}
	requiredName := openAIToolChoiceFunctionName(req.ToolChoice)
	calls := make([]openAIFunctionCall, 0, len(entries))
	for _, entry := range entries {
		tool, exists := allowed[entry.Name]
		if !exists {
			return nil, true, fmt.Errorf("Agent returned unknown function %q", entry.Name)
		}
		if requiredName != "" && entry.Name != requiredName {
			return nil, true, fmt.Errorf("Agent returned function %q, but tool_choice requires %q", entry.Name, requiredName)
		}
		arguments := entry.Arguments
		if len(arguments) == 0 || string(arguments) == "null" {
			arguments = json.RawMessage(`{}`)
		}
		var stringArguments string
		if json.Unmarshal(arguments, &stringArguments) == nil {
			if !json.Valid([]byte(stringArguments)) {
				return nil, true, fmt.Errorf("function %q returned invalid JSON arguments", entry.Name)
			}
		} else {
			compact := bytes.Buffer{}
			if err := json.Compact(&compact, arguments); err != nil {
				return nil, true, fmt.Errorf("function %q returned invalid arguments: %w", entry.Name, err)
			}
			stringArguments = compact.String()
		}
		var argumentsValue any
		decoder := json.NewDecoder(strings.NewReader(stringArguments))
		decoder.UseNumber()
		if err := decoder.Decode(&argumentsValue); err != nil {
			return nil, true, fmt.Errorf("function %q returned invalid arguments: %w", entry.Name, err)
		}
		if len(tool.Parameters) > 0 {
			if err := validateOpenAIJSONSchema(argumentsValue, tool.Parameters, tool.Parameters, "$", 0); err != nil {
				return nil, true, fmt.Errorf("function %q arguments do not match parameters: %w", entry.Name, err)
			}
		}
		calls = append(calls, openAIFunctionCall{
			ID: "fc_" + randHex(16), CallID: "call_" + randHex(12), Name: entry.Name, Arguments: stringArguments,
		})
	}
	return calls, true, nil
}

func normalizeOpenAIStructuredOutput(answer string, format openAIOutputFormat) (string, error) {
	if format.Type == "" || format.Type == "text" {
		return answer, nil
	}
	raw, err := extractOpenAIJSON(answer)
	if err != nil {
		return "", fmt.Errorf("structured output is not valid JSON: %w", err)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("structured output is not valid JSON: %w", err)
	}
	if format.Type == "json_object" {
		if _, ok := value.(map[string]any); !ok {
			return "", errors.New("structured output must be a JSON object")
		}
	}
	if format.Type == "json_schema" {
		if err := validateOpenAIJSONSchema(value, format.Schema, format.Schema, "$", 0); err != nil {
			return "", fmt.Errorf("structured output does not match schema: %w", err)
		}
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func extractOpenAIJSON(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 && strings.HasPrefix(lines[0], "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func validateOpenAIJSONSchema(value any, schema, root map[string]any, path string, depth int) error {
	if depth > 64 {
		return errors.New("schema nesting exceeds 64 levels")
	}
	if ref, _ := schema["$ref"].(string); ref != "" {
		resolved, err := resolveOpenAILocalRef(root, ref)
		if err != nil {
			return err
		}
		return validateOpenAIJSONSchema(value, resolved, root, path, depth+1)
	}
	if options, ok := schema["anyOf"].([]any); ok {
		for _, option := range options {
			if optionSchema, ok := option.(map[string]any); ok && validateOpenAIJSONSchema(value, optionSchema, root, path, depth+1) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s does not match anyOf", path)
	}
	if constant, exists := schema["const"]; exists && !reflect.DeepEqual(normalizeJSONNumber(value), normalizeJSONNumber(constant)) {
		return fmt.Errorf("%s does not equal const", path)
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(normalizeJSONNumber(value), normalizeJSONNumber(candidate)) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name, _ := rawName.(string)
				if _, exists := object[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		for name, item := range object {
			if property, exists := properties[name]; exists {
				if propertySchema, ok := property.(map[string]any); ok {
					if err := validateOpenAIJSONSchema(item, propertySchema, root, path+"."+name, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if additional, exists := schema["additionalProperties"]; exists {
				switch typed := additional.(type) {
				case bool:
					if !typed {
						return fmt.Errorf("%s.%s is not allowed", path, name)
					}
				case map[string]any:
					if err := validateOpenAIJSONSchema(item, typed, root, path+"."+name, depth+1); err != nil {
						return err
					}
				}
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range array {
				if err := validateOpenAIJSONSchema(item, itemSchema, root, path+"["+strconv.Itoa(index)+"]", depth+1); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok || strings.ContainsAny(number.String(), ".eE") {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "number":
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s must be a number", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	}
	return nil
}

func resolveOpenAILocalRef(root map[string]any, ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported schema reference %q", ref)
	}
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid schema reference %q", ref)
		}
		current, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("schema reference %q was not found", ref)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema reference %q is not an object", ref)
	}
	return resolved, nil
}

func normalizeJSONNumber(value any) any {
	if number, ok := value.(json.Number); ok {
		return number.String()
	}
	return value
}
