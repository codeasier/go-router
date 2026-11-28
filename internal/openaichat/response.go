package openaichat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/codeasier/go-router/internal/chatwire"
)

const maxResponse = 1 << 20

var (
	ErrHTTPStatus      = errors.New("OpenAI-compatible upstream returned a non-success status")
	ErrUpstreamError   = errors.New("OpenAI-compatible upstream returned an error")
	ErrInvalidResponse = errors.New("invalid OpenAI-compatible response")
	ErrIncompleteSSE   = errors.New("OpenAI-compatible stream did not complete")
	ErrDelivery        = errors.New("could not deliver OpenAI-compatible response")
)

type Usage = chatwire.Usage

// Observation is protocol evidence only, not an execution, accounting or
// retry classification. A nil Usage means unknown, not zero.
type Observation struct {
	HTTPStatus int
	Complete   bool
	Usage      *Usage
}

// Decode takes ownership of response.Body and closes it on every path.
// If emit is non-nil, it receives only validated JSON bytes or complete SSE
// data frames, synchronously. An error after an SSE frame means an incomplete
// stream, not authority to start another upstream request.
func (p *Prepared) Decode(ctx context.Context, response *http.Response, emit func([]byte) error) (Observation, error) {
	if emit == nil {
		return p.DecodeMedia(ctx, response, nil)
	}
	return p.DecodeMedia(ctx, response, func(_ string, data []byte) error { return emit(data) })
}

// DecodeMedia additionally tells the dispatcher the validated media type. A
// provider that defaults an omitted stream field to JSON may return JSON to an
// ordinary client that did not request SSE. Do not invent chunks, transcode,
// or retry; explicit modes continue to reject mismatched responses.
func (p *Prepared) DecodeMedia(ctx context.Context, response *http.Response, emit func(string, []byte) error) (Observation, error) {
	var out Observation
	if response == nil {
		return out, ErrInvalidResponse
	}
	out.HTTPStatus = response.StatusCode
	if response.Body != nil {
		defer response.Body.Close()
	}
	if p == nil || p.endpoint == "" || ctx == nil || response.Body == nil {
		return out, ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if response.StatusCode != http.StatusOK {
		// No body, Location metadata or provider-supplied message in errors.
		return out, ErrHTTPStatus
	}
	if len(response.Header.Values("Content-Type")) != 1 {
		return out, ErrInvalidResponse
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	acceptSSE := media == "text/event-stream" && p.stream
	acceptJSON := media == "application/json" && (!p.stream || p.allowImplicitJSON)
	if err != nil || (!acceptSSE && !acceptJSON) {
		return out, ErrInvalidResponse
	}

	var sink func([]byte) error
	if emit != nil {
		sink = func(data []byte) error { return emit(media, data) }
	}
	var usage *Usage
	if media == "text/event-stream" {
		usage, err = decodeStream(ctx, response.Body, sink)
	} else {
		usage, err = decodeJSON(ctx, response.Body, sink)
	}
	if err != nil {
		return out, err
	}
	out.Complete = true
	out.Usage = usage
	return out, nil
}

func decodeJSON(ctx context.Context, reader io.Reader, emit func([]byte) error) (*Usage, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponse+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(body) > maxResponse {
		return nil, ErrInvalidResponse
	}
	p, valid := chatwire.ParsePayload(body, "chat.completion")
	if !valid && errorPayload(body) {
		return nil, ErrUpstreamError
	}
	if !valid || len(p.Choices) == 0 {
		return nil, ErrInvalidResponse
	}
	usage := p.ConfirmedUsage()
	if emit != nil && emit(body) != nil {
		return nil, ErrDelivery
	}
	return usage, nil
}

func decodeStream(ctx context.Context, source io.Reader, emit func([]byte) error) (*Usage, error) {
	r := bufio.NewReaderSize(source, chatwire.MaxEvent)
	remaining := maxResponse
	var sawChoices, sawUsageFrame bool
	var usage *Usage
	for ctx.Err() == nil {
		event, ok := chatwire.NextEvent(r, &remaining)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if event.Error {
			return nil, ErrUpstreamError
		}
		if !event.HasData {
			continue
		}
		done := bytes.Equal(event.Data, []byte("[DONE]"))
		if done {
			if !sawChoices {
				return nil, ErrIncompleteSSE
			}
		} else {
			p, valid := chatwire.ParsePayload(event.Data, "chat.completion.chunk")
			if !valid && errorPayload(event.Data) {
				return nil, ErrUpstreamError
			}
			if !valid || sawUsageFrame {
				return nil, ErrIncompleteSSE
			}
			if len(p.Choices) != 0 {
				sawChoices = true
				// Some compatible endpoints report cumulative usage on
				// ordinary chunks. Only their separate final usage
				// frame is recognized by this generic adapter.
			} else {
				if !sawChoices || len(p.Usage) == 0 {
					return nil, ErrIncompleteSSE
				}
				sawUsageFrame = true
				usage = p.ConfirmedUsage()
			}
		}
		if emit != nil && emit(event.Raw) != nil {
			return nil, ErrDelivery
		}
		if done {
			return usage, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrIncompleteSSE
}

func errorPayload(body []byte) bool {
	// Never forward an upstream error object. A present error field, even
	// a poorly formed one, is not a successful response.
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	for key := range fields {
		if strings.EqualFold(key, "error") {
			return true
		}
	}
	return false
}
