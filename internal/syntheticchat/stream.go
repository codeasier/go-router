package syntheticchat

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
)

const maxEvent = 64 << 10

type event struct {
	raw     []byte
	data    []byte
	hasData bool
}

// nextEvent returns only fully delimited SSE frames. EOF within a frame is
// never a message or a terminal marker. Support LF, CRLF and multi-line data.
func nextEvent(r *bufio.Reader, remaining *int) (event, bool) {
	var e event
	for {
		line, err := r.ReadSlice('\n')
		if err != nil || len(e.raw)+len(line) > maxEvent || len(line) > *remaining {
			return event{}, false
		}
		*remaining -= len(line)
		e.raw = append(e.raw, line...)
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			return e, true
		}
		field, value, _ := bytes.Cut(line, []byte{':'})
		switch string(field) {
		case "":
			// SSE comment.
		case "data":
			if e.hasData {
				e.data = append(e.data, '\n')
			}
			e.hasData = true
			e.data = append(e.data, bytes.TrimPrefix(value, []byte{' '})...)
		default:
			// This fixture supports only plain OpenAI data frames and comments.
			return event{}, false
		}
	}
}

func forwardStream(ctx context.Context, w http.ResponseWriter, body io.Reader) (bool, *Usage) {
	r := bufio.NewReaderSize(body, maxEvent)
	remaining := maxReply
	started, anyChoice, finalUsageFrame := false, false, false
	var observed *Usage
	for ctx.Err() == nil {
		e, ok := nextEvent(r, &remaining)
		if !ok {
			break
		}
		if !e.hasData {
			continue
		}
		done := bytes.Equal(e.data, []byte("[DONE]"))
		if done {
			if !anyChoice {
				break
			}
		} else {
			p, parsed := parsePayload(e.data, "chat.completion.chunk")
			if !parsed || finalUsageFrame {
				break
			}
			if len(p.Choices) != 0 {
				anyChoice = true
			} else {
				if !anyChoice || len(p.Usage) == 0 {
					break
				}
				finalUsageFrame = true
				observed = p.confirmedUsage()
			}
		}

		if ctx.Err() != nil {
			break
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			started = true
		}
		_, err := w.Write(e.raw)
		if done {
			// Upstream completion is known even if the client disconnects
			// while we deliver the last verified frame.
			if err == nil {
				_ = http.NewResponseController(w).Flush()
			}
			return true, observed
		}
		if err != nil || http.NewResponseController(w).Flush() != nil {
			return false, nil
		}
	}

	if ctx.Err() == nil {
		if started {
			_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"code\":\"incomplete_upstream_stream\",\"type\":\"synthetic_chat_error\",\"message\":\"Synthetic stream did not complete\"}}\n\n")
			_ = http.NewResponseController(w).Flush()
		} else {
			writeError(w, http.StatusBadGateway, "invalid_upstream_response", "Invalid synthetic stream")
		}
	}
	return false, nil
}
