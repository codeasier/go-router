package syntheticchat

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/codeasier/go-router/internal/chatwire"
)

func forwardStream(ctx context.Context, w http.ResponseWriter, body io.Reader) (bool, *Usage) {
	r := bufio.NewReaderSize(body, chatwire.MaxEvent)
	remaining := maxReply
	started, anyChoice, finalUsageFrame := false, false, false
	var observed *Usage
	for ctx.Err() == nil {
		e, ok := chatwire.NextEvent(r, &remaining)
		if !ok || e.Error {
			break
		}
		if !e.HasData {
			continue
		}
		done := bytes.Equal(e.Data, []byte("[DONE]"))
		if done {
			if !anyChoice {
				break
			}
		} else {
			p, parsed := chatwire.ParsePayload(e.Data, "chat.completion.chunk")
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
				observed = p.ConfirmedUsage()
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
		_, err := w.Write(e.Raw)
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
