package openai

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

type sseEvent struct {
	Name string
	Data string
}

func readSSE(ctx context.Context, source io.Reader, maxEventBytes int, handle func(sseEvent) error) error {
	if ctx == nil || source == nil || handle == nil {
		return errors.New("read SSE: context, source, and callback are required")
	}
	if maxEventBytes <= 0 {
		return errors.New("read SSE: maximum event size must be positive")
	}
	reader := bufio.NewReaderSize(source, min(maxEventBytes, 64*1024))
	var name string
	var data []string
	eventBytes := 0
	dispatch := func() error {
		if len(data) == 0 {
			name = ""
			eventBytes = 0
			return nil
		}
		event := sseEvent{Name: name, Data: strings.Join(data, "\n")}
		name = ""
		data = nil
		eventBytes = 0
		return handle(event)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readSSELine(reader, maxEventBytes-eventBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) > 0 {
			eventBytes += len(line)
			if eventBytes > maxEventBytes {
				return errors.New("read SSE: event is too large")
			}
		}
		if len(line) == 0 {
			if dispatchError := dispatch(); dispatchError != nil {
				return dispatchError
			}
		} else if line[0] != ':' {
			field, value, found := strings.Cut(line, ":")
			if !found {
				field = line
				value = ""
			}
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name = value
			case "data":
				data = append(data, value)
			}
		}
		if errors.Is(err, io.EOF) {
			if len(data) > 0 {
				return dispatch()
			}
			return nil
		}
	}
}

func readSSELine(reader *bufio.Reader, remaining int) (string, error) {
	if remaining < 0 {
		return "", errors.New("read SSE: event is too large")
	}
	var line []byte
	for {
		fragment, prefix, err := reader.ReadLine()
		if len(line)+len(fragment) > remaining {
			return "", errors.New("read SSE: event is too large")
		}
		line = append(line, fragment...)
		if err != nil {
			return string(line), err
		}
		if !prefix {
			return string(line), nil
		}
	}
}
