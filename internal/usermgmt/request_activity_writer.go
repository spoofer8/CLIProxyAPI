package usermgmt

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	log "github.com/sirupsen/logrus"
)

var activityJournalLock sync.Mutex
var activeActivityJournals = make(map[string]bool)

type activityJournalRecord struct {
	Kind      string                 `json:"kind"`
	Item      *store.RequestActivity `json:"item,omitempty"`
	Direction string                 `json:"direction,omitempty"`
	Format    string                 `json:"format,omitempty"`
	Body      []byte                 `json:"body,omitempty"`
}

type activityJournal struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	aead   cipher.AEAD
	closed bool
}
type requestActivityWriter struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	wake          chan struct{}
	db            *store.Store
	directory     string
	aead          cipher.AEAD
	initErr       error
	retentionDays atomic.Int64 // Compatibility only: full content is never expired.
	replayMu      sync.Mutex
}

func newRequestActivityWriter(db *store.Store, days int, directories ...string) *requestActivityWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &requestActivityWriter{ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), db: db}
	w.retentionDays.Store(int64(days))
	directory := "activity-spool"
	if len(directories) > 0 && strings.TrimSpace(directories[0]) != "" {
		directory = directories[0]
	}
	w.initErr = w.initialize(directory)
	go w.run()
	return w
}

func (w *requestActivityWriter) initialize(directory string) error {
	activityJournalLock.Lock()
	defer activityJournalLock.Unlock()
	var identity string
	if err := w.db.DB().QueryRowContext(w.ctx, `SELECT current_database()||':'||current_schema()||':'||COALESCE(inet_server_addr()::text,'local')||':'||COALESCE(inet_server_port()::text,'local')`).Scan(&identity); err != nil {
		return errors.New("activity journal database identity unavailable")
	}
	digest := sha256.Sum256([]byte(identity))
	absolute, errPath := filepath.Abs(filepath.Join(directory, hex.EncodeToString(digest[:16])))
	if errPath != nil {
		return errors.New("activity journal directory unavailable")
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return errors.New("activity journal directory unavailable")
	}
	info, errInfo := os.Lstat(absolute)
	if errInfo != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("activity journal directory must be a real directory")
	}
	if err := os.Chmod(absolute, 0700); err != nil {
		return errors.New("activity journal permissions unavailable")
	}
	keyPath := filepath.Join(absolute, "journal.key")
	key, errRead := os.ReadFile(keyPath)
	if errors.Is(errRead, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return errors.New("activity journal key unavailable")
		}
		file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("activity journal key unavailable")
		}
		_, errWrite := file.Write(key)
		errSync := file.Sync()
		errClose := file.Close()
		if errWrite != nil || errSync != nil || errClose != nil {
			return errors.New("activity journal key persistence failed")
		}
		if err := syncActivityDirectory(absolute); err != nil {
			return err
		}
	} else if errRead != nil {
		return errors.New("activity journal key unavailable")
	}
	info, errInfo = os.Lstat(keyPath)
	if errInfo != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(key) != 32 {
		return errors.New("activity journal key permissions or format invalid")
	}
	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return errors.New("activity journal encryption unavailable")
	}
	w.aead, errCipher = cipher.NewGCM(block)
	w.directory = absolute
	return errCipher
}

func syncActivityDirectory(directory string) error {
	file, errOpen := os.Open(directory)
	if errOpen != nil {
		return errors.New("activity journal directory sync failed")
	}
	errSync := file.Sync()
	_ = file.Close()
	if errSync != nil {
		return errors.New("activity journal directory sync failed")
	}
	return nil
}

func (w *requestActivityWriter) begin(item store.RequestActivity) (*activityJournal, error) {
	if w == nil || w.initErr != nil || w.ctx.Err() != nil {
		return nil, errors.New("activity storage unavailable")
	}
	path := filepath.Join(w.directory, item.ID+".journal")
	activityJournalLock.Lock()
	defer activityJournalLock.Unlock()
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errOpen != nil {
		return nil, errors.New("activity journal admission failed")
	}
	journal := &activityJournal{file: file, path: path, aead: w.aead}
	if err := journal.append(activityJournalRecord{Kind: "begin", Item: &item}); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syncActivityDirectory(w.directory); err != nil {
		_ = file.Close()
		return nil, err
	}
	activeActivityJournals[path] = true
	return journal, nil
}

func (j *activityJournal) append(record activityJournalRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("activity journal closed")
	}
	// Each encryption frame is bounded; any number of frames may be persisted.
	if len(record.Body) > 64<<10 {
		body := record.Body
		for len(body) > 0 {
			part := min(len(body), 64<<10)
			record.Body = body[:part]
			if err := j.writeRecord(record); err != nil {
				return err
			}
			body = body[part:]
		}
	} else if err := j.writeRecord(record); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return errors.New("activity journal durable write failed")
	}
	return nil
}
func (j *activityJournal) writeRecord(record activityJournalRecord) error {
	plain, errJSON := json.Marshal(record)
	if errJSON != nil {
		return errors.New("activity journal encoding failed")
	}
	nonce := make([]byte, j.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return errors.New("activity journal nonce failed")
	}
	sealed := j.aead.Seal(nonce, nonce, plain, []byte(filepath.Base(j.path)))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if _, err := j.file.Write(length[:]); err != nil {
		return errors.New("activity journal write failed")
	}
	if _, err := j.file.Write(sealed); err != nil {
		return errors.New("activity journal write failed")
	}
	return nil
}
func (j *activityJournal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	errClose := j.file.Close()
	activityJournalLock.Lock()
	delete(activeActivityJournals, j.path)
	activityJournalLock.Unlock()
	return errClose
}
func (w *requestActivityWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if w.initErr == nil {
			if err := w.replayPending(w.ctx); err != nil && w.ctx.Err() == nil {
				log.Warn("user management activity recovery pending; durable journal retained")
			}
		}
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}
func (w *requestActivityWriter) replayPending(ctx context.Context) error {
	w.replayMu.Lock()
	defer w.replayMu.Unlock()
	entries, errRead := os.ReadDir(w.directory)
	if errRead != nil {
		return errors.New("activity recovery directory unavailable")
	}
	var failure error
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.HasSuffix(entry.Name(), ".journal") || !entry.Type().IsRegular() {
			continue
		}
		if _, err := ulid.ParseStrict(strings.TrimSuffix(entry.Name(), ".journal")); err != nil {
			continue
		}
		path := filepath.Join(w.directory, entry.Name())
		activityJournalLock.Lock()
		active := activeActivityJournals[path]
		activityJournalLock.Unlock()
		if active {
			continue
		}
		if err := w.replay(ctx, path); err != nil {
			failure = err
		}
	}
	return failure
}
func (w *requestActivityWriter) flush(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if w.initErr != nil {
		return w.initErr
	}
	return w.replayPending(ctx)
}
func (w *requestActivityWriter) close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return w.flush(ctx)
}
func readActivityJournalRecord(reader io.Reader, aead cipher.AEAD, name string) (activityJournalRecord, error) {
	var record activityJournalRecord
	var length [4]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return record, err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size < uint32(aead.NonceSize()+aead.Overhead()) || size > 2<<20 {
		return record, errors.New("invalid activity journal frame")
	}
	sealed := make([]byte, size)
	if _, err := io.ReadFull(reader, sealed); err != nil {
		return record, err
	}
	nonce := sealed[:aead.NonceSize()]
	plain, errOpen := aead.Open(nil, nonce, sealed[aead.NonceSize():], []byte(name))
	if errOpen != nil {
		return record, errors.New("activity journal authentication failed")
	}
	if err := json.Unmarshal(plain, &record); err != nil {
		return record, errors.New("invalid activity journal record")
	}
	return record, nil
}
