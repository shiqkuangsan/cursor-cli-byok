package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeConnectRequestAcceptsOneUncompressedMessage(t *testing.T) {
	want := []byte("protobuf")
	body := EncodeConnectMessage(want)
	got, err := DecodeConnectRequest(body, 1024)
	if err != nil {
		t.Fatalf("DecodeConnectRequest() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestReadConnectMessageHandlesFragmentedReaderAndEOF(t *testing.T) {
	want := []byte("fragmented protobuf")
	reader := &oneByteReader{source: bytes.NewReader(EncodeConnectMessage(want))}
	flag, got, err := ReadConnectMessage(reader, 1024)
	if err != nil {
		t.Fatalf("ReadConnectMessage() error = %v", err)
	}
	if flag != 0 || !bytes.Equal(got, want) {
		t.Fatalf("frame = flag %d payload %q", flag, got)
	}
	if _, _, err := ReadConnectMessage(reader, 1024); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadConnectMessage(EOF) error = %v", err)
	}
	if _, _, err := ReadConnectMessage(bytes.NewReader([]byte{0, 0, 0}), 1024); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("ReadConnectMessage(truncated) error = %v", err)
	}
}

type oneByteReader struct {
	source *bytes.Reader
}

func (reader *oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return reader.source.Read(buffer)
}

func TestDecodeConnectRequestRejectsInvalidEnvelopes(t *testing.T) {
	message := EncodeConnectMessage([]byte("message"))
	compressed := append([]byte(nil), message...)
	compressed[0] = 0x01
	endStream := append([]byte(nil), message...)
	endStream[0] = 0x02
	trailing := append(append([]byte(nil), message...), message...)
	oversized := EncodeConnectMessage([]byte("too large"))
	truncated := append([]byte(nil), message[:len(message)-1]...)
	lengthOverflow := make([]byte, 5)
	binary.BigEndian.PutUint32(lengthOverflow[1:], 2048)

	tests := []struct {
		name string
		body []byte
		max  int
		want string
	}{
		{name: "short header", body: []byte{0}, max: 1024, want: "header"},
		{name: "compressed", body: compressed, max: 1024, want: "compressed"},
		{name: "end stream", body: endStream, max: 1024, want: "message flag"},
		{name: "trailing frame", body: trailing, max: 1024, want: "exactly one"},
		{name: "oversized", body: oversized, max: 3, want: "too large"},
		{name: "truncated", body: truncated, max: 1024, want: "length"},
		{name: "declared oversized", body: lengthOverflow, max: 1024, want: "too large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeConnectRequest(test.body, test.max)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeConnectRequest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEncodeConnectEndUsesConnectEndStreamJSON(t *testing.T) {
	success := EncodeConnectEnd("", "")
	if len(success) < 5 || success[0] != 0x02 {
		t.Fatalf("success frame = %x, want end-stream envelope", success)
	}
	var successPayload struct {
		Metadata map[string][]string `json:"metadata"`
	}
	if err := json.Unmarshal(success[5:], &successPayload); err != nil {
		t.Fatalf("Unmarshal(success) error = %v", err)
	}
	if successPayload.Metadata == nil {
		t.Fatal("success metadata is nil")
	}

	failure := EncodeConnectEnd("internal", "provider failed")
	var failurePayload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(failure[5:], &failurePayload); err != nil {
		t.Fatalf("Unmarshal(failure) error = %v", err)
	}
	if failurePayload.Error.Code != "internal" || failurePayload.Error.Message != "provider failed" {
		t.Fatalf("failure payload = %#v", failurePayload)
	}
}
