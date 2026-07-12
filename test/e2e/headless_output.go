package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const maxHeadlessEventBytes = 4 << 20

type headlessResult struct {
	Type          string                     `json:"type"`
	Subtype       string                     `json:"subtype"`
	IsError       *bool                      `json:"is_error"`
	DurationMS    *json.Number               `json:"duration_ms"`
	DurationAPIMS *json.Number               `json:"duration_api_ms"`
	Result        string                     `json:"result"`
	SessionID     string                     `json:"session_id"`
	RequestID     string                     `json:"request_id"`
	Usage         map[string]json.RawMessage `json:"usage"`
}

type headlessStreamEvent struct {
	headlessResult
	Message *headlessMessage `json:"message"`
}

type headlessMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func validateHeadlessJSON(reader io.Reader, expected string) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var result headlessResult
	if err := decoder.Decode(&result); err != nil {
		return errors.New("decode result JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("result JSON contains trailing output")
	}
	return validateHeadlessResult(result, expected)
}

func validateHeadlessResult(result headlessResult, expected string) error {
	if result.Type != "result" {
		return errors.New("result type is not result")
	}
	if result.Subtype != "success" {
		return errors.New("result subtype is not success")
	}
	if result.IsError == nil || *result.IsError {
		return errors.New("result is_error is not false")
	}
	if result.Result != expected {
		return errors.New("result text does not match expected text")
	}
	if result.SessionID == "" {
		return errors.New("result session_id is missing")
	}
	if result.RequestID == "" {
		return errors.New("result request_id is missing")
	}
	for _, name := range []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens"} {
		value, ok := result.Usage[name]
		if !ok {
			return fmt.Errorf("result usage.%s is missing", name)
		}
		if err := validateNonNegativeInteger(value); err != nil {
			return fmt.Errorf("result usage.%s is invalid", name)
		}
	}
	for name, duration := range map[string]*json.Number{
		"duration_ms":     result.DurationMS,
		"duration_api_ms": result.DurationAPIMS,
	} {
		if duration == nil {
			continue
		}
		value := json.RawMessage(duration.String())
		if err := validateNonNegativeInteger(value); err != nil {
			return fmt.Errorf("result %s is invalid", name)
		}
	}
	return nil
}

func validateNonNegativeInteger(raw json.RawMessage) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return err
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return errors.New("not a non-negative integer")
	}
	return nil
}

func validateHeadlessStreamJSON(reader io.Reader, expected string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxHeadlessEventBytes)
	var sessionID string
	var assistantTexts []string
	lineNumber := 0
	seenInit := false
	seenUser := false
	seenResult := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lineNumber++
		if seenResult {
			return errors.New("stream contains an event after the terminal result")
		}
		var event headlessStreamEvent
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			return fmt.Errorf("stream line %d is not valid JSON", lineNumber)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return fmt.Errorf("stream line %d contains trailing output", lineNumber)
		}
		if event.SessionID != "" {
			if sessionID == "" {
				sessionID = event.SessionID
			} else if event.SessionID != sessionID {
				return errors.New("stream session_id changed between events")
			}
		}

		switch event.Type {
		case "system":
			if lineNumber != 1 || seenInit || event.Subtype != "init" {
				return errors.New("stream system/init event is invalid")
			}
			seenInit = true
		case "user":
			if !seenInit || seenUser || len(assistantTexts) != 0 {
				return errors.New("stream user event is out of order")
			}
			if event.Message == nil || event.Message.Role != "user" {
				return errors.New("stream user message is invalid")
			}
			seenUser = true
		case "assistant":
			if !seenUser {
				return errors.New("stream assistant event is out of order")
			}
			if event.Message == nil || event.Message.Role != "assistant" {
				return errors.New("stream assistant message is invalid")
			}
			text, err := headlessMessageText(event.Message.Content)
			if err != nil {
				return err
			}
			assistantTexts = append(assistantTexts, text)
		case "result":
			if !seenInit || !seenUser || len(assistantTexts) < 2 {
				return errors.New("stream result event is out of order")
			}
			if assistantTexts[len(assistantTexts)-1] != expected {
				return errors.New("stream final assistant text does not match expected text")
			}
			if strings.Join(assistantTexts[:len(assistantTexts)-1], "") != expected {
				return errors.New("stream partial assistant text does not match expected text")
			}
			if err := validateHeadlessResult(event.headlessResult, expected); err != nil {
				return err
			}
			seenResult = true
		default:
			return errors.New("stream event type is unsupported")
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("read stream JSON")
	}
	if !seenResult {
		return errors.New("stream terminal result is missing")
	}
	return nil
}

func headlessMessageText(content json.RawMessage) (string, error) {
	if len(content) == 0 {
		return "", errors.New("stream message content is missing")
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text, nil
	}
	var blocks []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return "", errors.New("stream message content is invalid")
	}
	var combined strings.Builder
	foundText := false
	for _, block := range blocks {
		if block.Type != "text" || block.Text == nil {
			continue
		}
		foundText = true
		_, _ = combined.WriteString(*block.Text)
	}
	if !foundText {
		return "", errors.New("stream message text is missing")
	}
	return combined.String(), nil
}

func runHeadlessOutputCheck(args []string, stdin io.Reader) error {
	flags := flag.NewFlagSet("output-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var format string
	var expected string
	var rejectSuccess bool
	flags.StringVar(&format, "format", "", "output format")
	flags.StringVar(&expected, "expected", "", "expected final text")
	flags.BoolVar(&rejectSuccess, "reject-success", false, "reject successful result events")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid options")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if rejectSuccess {
		if expected != "" {
			return errors.New("--expected and --reject-success cannot be combined")
		}
		return rejectSuccessfulHeadlessResult(stdin, format)
	}
	if expected == "" {
		return errors.New("--expected is required")
	}
	switch format {
	case "json":
		return validateHeadlessJSON(stdin, expected)
	case "stream-json":
		return validateHeadlessStreamJSON(stdin, expected)
	default:
		return errors.New("--format must be json or stream-json")
	}
}

func rejectSuccessfulHeadlessResult(reader io.Reader, format string) error {
	check := func(event headlessResult) error {
		if event.Type == "result" && event.Subtype == "success" {
			return errors.New("machine output contains a successful result")
		}
		return nil
	}
	decode := func(reader io.Reader) (headlessResult, error) {
		decoder := json.NewDecoder(reader)
		decoder.UseNumber()
		var event headlessResult
		if err := decoder.Decode(&event); err != nil {
			return headlessResult{}, errors.New("decode machine output JSON")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return headlessResult{}, errors.New("machine output JSON contains trailing output")
		}
		return event, nil
	}

	switch format {
	case "json":
		event, err := decode(reader)
		if err != nil {
			return err
		}
		return check(event)
	case "stream-json":
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), maxHeadlessEventBytes)
		seenEvent := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			event, err := decode(strings.NewReader(line))
			if err != nil {
				return err
			}
			seenEvent = true
			if err := check(event); err != nil {
				return err
			}
		}
		if err := scanner.Err(); err != nil {
			return errors.New("read stream JSON")
		}
		if !seenEvent {
			return errors.New("machine output stream contains no events")
		}
		return nil
	default:
		return errors.New("--format must be json or stream-json")
	}
}
