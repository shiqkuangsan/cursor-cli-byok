package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const connectEnvelopeHeaderBytes = 5

func EncodeConnectMessage(payload []byte) []byte {
	frame := make([]byte, connectEnvelopeHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame[1:connectEnvelopeHeaderBytes], uint32(len(payload)))
	copy(frame[connectEnvelopeHeaderBytes:], payload)
	return frame
}

func DecodeConnectRequest(body []byte, maxMessageBytes int) ([]byte, error) {
	if maxMessageBytes <= 0 {
		return nil, errors.New("decode connect request: maximum message size must be positive")
	}
	if len(body) < connectEnvelopeHeaderBytes {
		return nil, errors.New("decode connect request: incomplete envelope header")
	}
	switch body[0] {
	case 0:
	case 1:
		return nil, errors.New("decode connect request: compressed messages are unsupported")
	default:
		return nil, errors.New("decode connect request: invalid message flag")
	}
	messageLength := uint64(binary.BigEndian.Uint32(body[1:connectEnvelopeHeaderBytes]))
	if messageLength > uint64(maxMessageBytes) {
		return nil, errors.New("decode connect request: message is too large")
	}
	wantLength := uint64(connectEnvelopeHeaderBytes) + messageLength
	if uint64(len(body)) != wantLength {
		if uint64(len(body)) > wantLength {
			return nil, errors.New("decode connect request: expected exactly one message")
		}
		return nil, fmt.Errorf("decode connect request: envelope length is %d, body has %d bytes", messageLength, len(body)-connectEnvelopeHeaderBytes)
	}
	return append([]byte(nil), body[connectEnvelopeHeaderBytes:]...), nil
}

func ReadConnectMessage(reader io.Reader, maxMessageBytes int) (byte, []byte, error) {
	if reader == nil {
		return 0, nil, errors.New("read connect message: reader is required")
	}
	if maxMessageBytes <= 0 {
		return 0, nil, errors.New("read connect message: maximum message size must be positive")
	}
	header := make([]byte, connectEnvelopeHeaderBytes)
	read, err := io.ReadFull(reader, header)
	if errors.Is(err, io.EOF) && read == 0 {
		return 0, nil, io.EOF
	}
	if err != nil {
		return 0, nil, errors.New("read connect message: incomplete envelope header")
	}
	flag := header[0]
	if flag&0x01 != 0 {
		return 0, nil, errors.New("read connect message: compressed messages are unsupported")
	}
	if flag != 0 && flag != 0x02 {
		return 0, nil, errors.New("read connect message: invalid envelope flag")
	}
	messageLength := uint64(binary.BigEndian.Uint32(header[1:]))
	if messageLength > uint64(maxMessageBytes) {
		return 0, nil, errors.New("read connect message: message is too large")
	}
	payload := make([]byte, int(messageLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, errors.New("read connect message: incomplete envelope body")
	}
	return flag, payload, nil
}

func EncodeConnectEnd(code, message string) []byte {
	payload := connectEndPayload{Metadata: map[string][]string{}}
	if code != "" {
		payload.Error = &connectEndError{Code: code, Message: message}
	}
	encoded, _ := json.Marshal(payload)
	frame := EncodeConnectMessage(encoded)
	frame[0] = 0x02
	return frame
}

type connectEndPayload struct {
	Error    *connectEndError    `json:"error,omitempty"`
	Metadata map[string][]string `json:"metadata"`
}

type connectEndError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
