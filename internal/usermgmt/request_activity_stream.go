package usermgmt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

// Replay makes metadata and content passes over encrypted frames. Raw and
// decoded bodies are never held simultaneously; SSE output is handled per event.
type activityDirectionReader struct {
	file            *os.File
	aead            cipher.AEAD
	name, direction string
	pending         []byte
}

func newActivityDirectionReader(path string, aead cipher.AEAD, direction string) (*activityDirectionReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("activity content recovery unavailable")
	}
	return &activityDirectionReader{file: file, aead: aead, name: filepath.Base(path), direction: direction}, nil
}
func (r *activityDirectionReader) Close() error { return r.file.Close() }
func (r *activityDirectionReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		record, err := readActivityJournalRecord(r.file, r.aead, r.name)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		if record.Kind == "content" && record.Direction == r.direction {
			r.pending = record.Body
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type activityContentSink struct {
	ctx         context.Context
	db          *store.Store
	id, format  string
	part, total int64
	pending     []byte
	preview     []byte
	responseID  string
}

func (s *activityContentSink) write(body []byte) error {
	if len(s.preview) < 4096 {
		s.preview = append(s.preview, body[:min(len(body), 4096-len(s.preview))]...)
	}
	s.total += int64(len(body))
	for len(body) > 0 {
		count := min(len(body), (64<<10)-len(s.pending))
		s.pending = append(s.pending, body[:count]...)
		body = body[count:]
		if len(s.pending) == 64<<10 {
			cut := len(s.pending)
			for cut > 0 && !utf8.Valid(s.pending[:cut]) {
				cut--
			}
			if cut == 0 {
				return errors.New("activity content encoding invalid")
			}
			if err := s.save(s.pending[:cut]); err != nil {
				return err
			}
			s.pending = append(s.pending[:0], s.pending[cut:]...)
		}
	}
	return nil
}
func (s *activityContentSink) save(body []byte) error {
	if err := s.db.SaveRequestContentChunk(s.ctx, s.id, "response", s.format, s.part, string(body)); err != nil {
		return err
	}
	s.part++
	return nil
}
func (s *activityContentSink) flush() error {
	if len(s.pending) > 0 {
		return s.save(s.pending)
	}
	return nil
}
func (s *activityContentSink) event(body []byte) error {
	safe, _ := sanitizeCompleteActivityJSON(body)
	if len(safe) == 0 {
		return nil
	}
	if id := capturedResponseID(safe); id != "" {
		s.responseID = id
	}
	if err := s.write(safe); err != nil {
		return err
	}
	if s.format == "jsonl" {
		return s.write([]byte("\n"))
	}
	return nil
}

func (w *requestActivityWriter) saveResponseContent(ctx context.Context, item *store.RequestActivity, source io.Reader, format string) error {
	reader := bufio.NewReader(source)
	prefix, _ := reader.Peek(6)
	isSSE := format == "sse" || bytes.HasPrefix(prefix, []byte("data:")) || bytes.HasPrefix(prefix, []byte("event:"))
	sink := &activityContentSink{ctx: ctx, db: w.db, id: item.ID, format: "json"}
	if isSSE || format == "ws" {
		sink.format = "jsonl"
	}
	if isSSE {
		var event bytes.Buffer
		flush := func() error {
			payload := bytes.TrimSpace(event.Bytes())
			defer event.Reset()
			if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
				return sink.event(payload)
			}
			return nil
		}
		for {
			line, errRead := reader.ReadString('\n')
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				if err := flush(); err != nil {
					return err
				}
			} else if strings.HasPrefix(line, "data:") {
				if event.Len() > 0 {
					event.WriteByte('\n')
				}
				event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			if errRead != nil {
				if err := flush(); err != nil {
					return err
				}
				if errRead != io.EOF {
					return errRead
				}
				break
			}
		}
	} else {
		decoder := json.NewDecoder(reader)
		decoder.UseNumber()
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				if err != io.EOF {
					if errWrite := sink.event([]byte(`{"_omitted":"invalid_or_incomplete_json"}`)); errWrite != nil {
						return errWrite
					}
				}
				break
			}
			if err := sink.event(raw); err != nil {
				return err
			}
		}
	}
	if err := sink.flush(); err != nil {
		return err
	}
	item.ResponsePreview = activityText(string(sink.preview), 4096)
	item.ResponseID = sink.responseID
	item.ResponseContentBytes = sink.total
	return nil
}
