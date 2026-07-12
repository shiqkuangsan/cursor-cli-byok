package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

const validHeadlessJSON = `{"type":"result","subtype":"success","is_error":false,"duration_ms":37,"duration_api_ms":19,"result":"JSON_OK","session_id":"session","request_id":"request","usage":{"inputTokens":1,"outputTokens":2,"cacheReadTokens":0,"cacheWriteTokens":0}}`

func TestValidateHeadlessJSONAcceptsSuccessfulResult(t *testing.T) {
	if err := validateHeadlessJSON(strings.NewReader(validHeadlessJSON+"\n"), "JSON_OK"); err != nil {
		t.Fatalf("validateHeadlessJSON() error = %v", err)
	}
}

func TestValidateHeadlessJSONRejectsInvalidResults(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "malformed", input: `{"type":`},
		{name: "trailing output", input: validHeadlessJSON + "\nnot-json"},
		{name: "wrong type", input: strings.Replace(validHeadlessJSON, `"type":"result"`, `"type":"assistant"`, 1)},
		{name: "wrong subtype", input: strings.Replace(validHeadlessJSON, `"subtype":"success"`, `"subtype":"error"`, 1)},
		{name: "error result", input: strings.Replace(validHeadlessJSON, `"is_error":false`, `"is_error":true`, 1)},
		{name: "wrong text", input: strings.Replace(validHeadlessJSON, `"result":"JSON_OK"`, `"result":"WRONG"`, 1)},
		{name: "missing session", input: strings.Replace(validHeadlessJSON, `"session_id":"session",`, "", 1)},
		{name: "missing request", input: strings.Replace(validHeadlessJSON, `"request_id":"request",`, "", 1)},
		{name: "missing usage", input: strings.Replace(validHeadlessJSON, `,"usage":{"inputTokens":1,"outputTokens":2,"cacheReadTokens":0,"cacheWriteTokens":0}`, "", 1)},
		{name: "negative usage", input: strings.Replace(validHeadlessJSON, `"inputTokens":1`, `"inputTokens":-1`, 1)},
		{name: "negative duration", input: strings.Replace(validHeadlessJSON, `"duration_ms":37`, `"duration_ms":-1`, 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateHeadlessJSON(strings.NewReader(test.input), "JSON_OK"); err == nil {
				t.Fatal("validateHeadlessJSON() error = nil, want rejection")
			}
		})
	}
}

func validHeadlessStream() string {
	return strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"session"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"prompt"}]},"session_id":"session"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"STREAM"}]},"session_id":"session","timestamp_ms":100}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"_OK"}]},"session_id":"session","timestamp_ms":101}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"STREAM_OK"}]},"session_id":"session"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"STREAM_OK","session_id":"session","request_id":"request","usage":{"inputTokens":3,"outputTokens":2,"cacheReadTokens":0,"cacheWriteTokens":0}}`,
	}, "\n") + "\n"
}

func TestValidateHeadlessStreamJSONAcceptsOrderedPartialOutput(t *testing.T) {
	input := "\n" + validHeadlessStream() + "\n"
	if err := validateHeadlessStreamJSON(strings.NewReader(input), "STREAM_OK"); err != nil {
		t.Fatalf("validateHeadlessStreamJSON() error = %v", err)
	}
}

func TestValidateHeadlessStreamJSONRejectsInvalidSequences(t *testing.T) {
	valid := validHeadlessStream()
	lines := strings.Split(strings.TrimSuffix(valid, "\n"), "\n")
	withoutInit := strings.Join(lines[1:], "\n") + "\n"
	withoutPartialsLines := append(append([]string{}, lines[:2]...), lines[4:]...)
	withoutPartials := strings.Join(withoutPartialsLines, "\n") + "\n"
	withoutResult := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	tests := []struct {
		name  string
		input string
	}{
		{name: "malformed line", input: strings.Replace(valid, `{"type":"user"`, `{"type":`, 1)},
		{name: "user before init", input: withoutInit},
		{name: "no partial output", input: withoutPartials},
		{name: "partial text mismatch", input: strings.Replace(valid, `"text":"_OK"`, `"text":"_NO"`, 1)},
		{name: "final text mismatch", input: strings.Replace(valid, `"text":"STREAM_OK"`, `"text":"WRONG"`, 1)},
		{name: "inconsistent session", input: strings.Replace(valid, `"session_id":"session","timestamp_ms":100`, `"session_id":"other","timestamp_ms":100`, 1)},
		{name: "error result", input: strings.Replace(valid, `"is_error":false`, `"is_error":true`, 1)},
		{name: "missing result", input: withoutResult},
		{name: "event after result", input: valid + `{"type":"assistant","message":{"role":"assistant","content":"late"},"session_id":"session"}` + "\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateHeadlessStreamJSON(strings.NewReader(test.input), "STREAM_OK"); err == nil {
				t.Fatal("validateHeadlessStreamJSON() error = nil, want rejection")
			}
		})
	}
}

func TestE2EHelperOutputCheckDispatch(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		var stderr bytes.Buffer
		code := runE2EHelper(context.Background(), []string{"output-check", "--format", "json", "--expected", "JSON_OK"}, strings.NewReader(validHeadlessJSON), io.Discard, &stderr, nil)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
		}
	})

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing format", args: []string{"output-check", "--expected", "JSON_OK"}},
		{name: "invalid format", args: []string{"output-check", "--format", "xml", "--expected", "JSON_OK"}},
		{name: "missing expected", args: []string{"output-check", "--format", "json"}},
		{name: "unexpected argument", args: []string{"output-check", "--format", "json", "--expected", "JSON_OK", "extra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runE2EHelper(context.Background(), test.args, strings.NewReader(validHeadlessJSON), io.Discard, &stderr, nil)
			if code == 0 || !strings.Contains(stderr.String(), "output-check") {
				t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestE2EHelperOutputCheckDoesNotRepeatRejectedPayload(t *testing.T) {
	const sensitivePayload = "TOP_SECRET_PAYLOAD"
	input := strings.Replace(validHeadlessJSON, "JSON_OK", sensitivePayload, 1)
	var stderr bytes.Buffer
	code := runE2EHelper(context.Background(), []string{"output-check", "--format", "json", "--expected", "JSON_OK"}, strings.NewReader(input), io.Discard, &stderr, nil)
	if code == 0 {
		t.Fatal("runE2EHelper() code = 0, want validator failure")
	}
	if strings.Contains(stderr.String(), sensitivePayload) {
		t.Fatalf("stderr repeated rejected payload: %q", stderr.String())
	}
}

func TestE2EHelperOutputCheckRejectSuccessMode(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input string
	}{
		{name: "rejects empty stream"},
		{name: "rejects blank-only stream", input: "\n  \n\t\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := runE2EHelper(context.Background(), []string{"output-check", "--format", "stream-json", "--reject-success"}, strings.NewReader(testCase.input), io.Discard, &stderr, nil)
			if code == 0 || !strings.Contains(stderr.String(), "no events") {
				t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
			}
		})
	}

	t.Run("accepts failure events", func(t *testing.T) {
		input := strings.Join([]string{
			`{"type":"system","subtype":"init"}`,
			`{"is_error":true,"subtype":"error","type":"result"}`,
		}, "\n") + "\n"
		var stderr bytes.Buffer
		code := runE2EHelper(context.Background(), []string{"output-check", "--format", "stream-json", "--reject-success"}, strings.NewReader(input), io.Discard, &stderr, nil)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
		}
	})

	t.Run("rejects reordered success fields", func(t *testing.T) {
		input := `{"is_error":false,"subtype":"success","type":"result"}` + "\n"
		var stderr bytes.Buffer
		code := runE2EHelper(context.Background(), []string{"output-check", "--format", "stream-json", "--reject-success"}, strings.NewReader(input), io.Discard, &stderr, nil)
		if code == 0 || !strings.Contains(stderr.String(), "successful result") {
			t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
		}
	})

	t.Run("rejects malformed machine output", func(t *testing.T) {
		var stderr bytes.Buffer
		code := runE2EHelper(context.Background(), []string{"output-check", "--format", "stream-json", "--reject-success"}, strings.NewReader("not-json\n"), io.Discard, &stderr, nil)
		if code == 0 {
			t.Fatalf("runE2EHelper() code = 0, stderr = %q", stderr.String())
		}
	})

	t.Run("rejects conflicting modes", func(t *testing.T) {
		var stderr bytes.Buffer
		code := runE2EHelper(context.Background(), []string{"output-check", "--format", "stream-json", "--expected", "OK", "--reject-success"}, strings.NewReader(""), io.Discard, &stderr, nil)
		if code == 0 {
			t.Fatalf("runE2EHelper() code = 0, stderr = %q", stderr.String())
		}
	})
}
