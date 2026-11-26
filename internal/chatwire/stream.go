package chatwire

import (
	"bufio"
	"bytes"
)

const MaxEvent = 64 << 10

type Event struct {
	Raw     []byte
	Data    []byte
	HasData bool
	Error   bool
}

// NextEvent returns only fully delimited SSE frames. EOF within a frame is
// never a message or a terminal marker. Support LF, CRLF and multi-line data.
func NextEvent(r *bufio.Reader, remaining *int) (Event, bool) {
	var e Event
	for {
		line, err := r.ReadSlice('\n')
		if err != nil || len(e.Raw)+len(line) > MaxEvent || len(line) > *remaining {
			return Event{}, false
		}
		*remaining -= len(line)
		e.Raw = append(e.Raw, line...)
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
			if e.HasData {
				e.Data = append(e.Data, '\n')
			}
			e.HasData = true
			e.Data = append(e.Data, bytes.TrimPrefix(value, []byte{' '})...)
		case "event":
			if e.Error || !bytes.Equal(bytes.TrimPrefix(value, []byte{' '}), []byte("error")) {
				return Event{}, false
			}
			e.Error = true
		default:
			return Event{}, false
		}
	}
}
