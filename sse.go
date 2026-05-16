package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type SSEEvent struct {
	Event string
	Data  string
}

func readSSE(r io.Reader, emit func(SSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := emitSSEBlock(lines, emit); err != nil {
				return err
			}
			lines = nil
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return emitSSEBlock(lines, emit)
}

func emitSSEBlock(lines []string, emit func(SSEEvent) error) error {
	if len(lines) == 0 {
		return nil
	}
	var event SSEEvent
	var data []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	event.Data = strings.Join(data, "\n")
	if event.Data == "" && event.Event == "" {
		return nil
	}
	return emit(event)
}

func writeSSE(w io.Writer, data any) error {
	if dataString, ok := data.(string); ok && dataString == "[DONE]" {
		_, err := io.WriteString(w, "data: [DONE]\n\n")
		return err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(append([]byte("data: "), append(encoded, '\n', '\n')...)))
	return err
}
