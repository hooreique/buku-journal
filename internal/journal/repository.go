// Package journal keeps ordinary text files as the source of truth and jj as
// their history. No app-specific metadata is added to a journal entry.
package journal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrConflict     = errors.New("기록이 다른 곳에서 수정되었습니다. 최신 내용을 확인한 뒤 다시 시도해 주세요")
	ErrInvalid      = errors.New("유효하지 않은 기록 파일입니다")
	entryPattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-.+\.md$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

const maxFileSize = 1024 * 1024
const maxSnapshotSize = 32 * 1024 * 1024

// Entry is a dated Markdown file. ID is its filename, not hidden metadata.
type Entry struct {
	ID    string `json:"id"`
	Date  string `json:"date"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Hash  string `json:"hash"`
}
type Place struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	URL     string `json:"url"`
}
type Change struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
}
type Repository struct {
	Dir    string
	jjPath string
	mu     sync.Mutex
}

type transaction struct {
	ID          string            `json:"id"`
	Base        map[string][]byte `json:"base"`
	Next        map[string][]byte `json:"next"`
	Description string            `json:"description"`
}

// Open creates an independent jj repository if needed and recovers an
// interrupted app transaction. A divergent manual edit is never overwritten.
func Open(dir, jjPath string) (*Repository, error) {
	if jjPath == "" {
		jjPath = "jj"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	r := &Repository{Dir: abs, jjPath: jjPath}
	info, err := os.Lstat(filepath.Join(abs, ".jj"))
	if errors.Is(err, os.ErrNotExist) {
		if _, err = r.command("git", "init", abs); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("invalid .jj directory")
	}
	err = r.locked(func() error { return r.recoverLocked() })
	return r, err
}

func (r *Repository) command(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base := []string{"--color", "never", "--no-pager", "--quiet", "--config", `user.name="Buku Journal"`, "--config", `user.email="journal@localhost"`, "--config", `signing.behavior="drop"`}
	cmd := exec.CommandContext(ctx, r.jjPath, append(base, args...)...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "JJ_EDITOR=true", "PAGER=cat")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("jj %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (r *Repository) locked(fn func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fd, err := syscall.Open(filepath.Join(r.Dir, ".jj", "buku-journal.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err = syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	return fn()
}

// ValidFilename restricts journal files to a flat, portable namespace.
func ValidFilename(name string) bool {
	if name == "" || len(name) > 240 || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") || !utf8.ValidString(name) {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	if strings.HasSuffix(name, ".txt") {
		return len(name) > 4
	}
	if !entryPattern.MatchString(name) {
		return false
	}
	_, err := time.Parse("2006-01-02", name[:10])
	return err == nil
}
func regularRead(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize || !utf8.Valid(data) {
		return nil, ErrInvalid
	}
	return data, nil
}
func snapshot(dir string) (map[string][]byte, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := map[string][]byte{}
	total := 0
	for _, f := range files {
		if !ValidFilename(f.Name()) {
			continue
		}
		data, err := regularRead(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		total += len(data)
		if total > maxSnapshotSize {
			return nil, errors.New("기록 크기가 너무 큽니다")
		}
		result[f.Name()] = data
	}
	return result, nil
}
func (r *Repository) Snapshot() (files map[string][]byte, err error) {
	err = r.locked(func() error {
		if err := r.recoverLocked(); err != nil {
			return err
		}
		var e error
		files, e = snapshot(r.Dir)
		return e
	})
	return
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func snapshotHash(files map[string][]byte) string {
	h := sha256.New()
	for _, name := range names(files) {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(digest(files[name])))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (r *Repository) List() (entries []Entry, revision string, err error) {
	files, err := r.Snapshot()
	if err != nil {
		return nil, "", err
	}
	entries = []Entry{}
	for name, data := range files {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		entries = append(entries, Entry{ID: name, Date: name[:10], Title: strings.ReplaceAll(strings.TrimSuffix(name[11:], ".md"), "-", " · "), Body: string(data), Hash: digest(data)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID > entries[j].ID })
	return entries, snapshotHash(files), nil
}
func (r *Repository) Place(id string) (Place, error) {
	if !ValidFilename(id) || !strings.HasSuffix(id, ".txt") {
		return Place{}, ErrInvalid
	}
	files, err := r.Snapshot()
	if err != nil {
		return Place{}, err
	}
	data, ok := files[id]
	if !ok {
		return Place{}, os.ErrNotExist
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
	p := Place{ID: id, Name: strings.TrimSuffix(id, ".txt")}
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		p.Name = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		p.Address = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		p.URL = strings.TrimSpace(lines[2])
	}
	return p, nil
}

func names(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func equal(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}
func sameFile(a, b map[string][]byte, k string) bool {
	av, ao := a[k]
	bv, bo := b[k]
	return ao == bo && bytes.Equal(av, bv)
}
func clone(m map[string][]byte) map[string][]byte {
	n := make(map[string][]byte, len(m))
	for k, v := range m {
		n[k] = bytes.Clone(v)
	}
	return n
}
func validate(files map[string][]byte) error {
	total := 0
	for name, data := range files {
		if !ValidFilename(name) || len(data) > maxFileSize || !utf8.Valid(data) {
			return fmt.Errorf("%w: %s", ErrInvalid, name)
		}
		total += len(data)
	}
	if total > maxSnapshotSize {
		return ErrInvalid
	}
	return nil
}

// Apply compares the entire base snapshot, saves a durable transaction and
// commits the validated changes. Each successful call is one new jj change.
func (r *Repository) Apply(base, next map[string][]byte, description string) (id string, err error) {
	if err = validate(base); err != nil {
		return
	}
	if err = validate(next); err != nil {
		return
	}
	err = r.locked(func() error {
		if err := r.recoverLocked(); err != nil {
			return err
		}
		current, err := snapshot(r.Dir)
		if err != nil {
			return err
		}
		if !equal(current, base) {
			return ErrConflict
		}
		if equal(base, next) {
			return errors.New("변경된 내용이 없습니다")
		}
		if err = r.captureExternalLocked(); err != nil {
			return err
		}
		// Snapshot once more after jj captured manual edits; editors do not honor
		// our lock, so never assume the previous filesystem read is still current.
		current, err = snapshot(r.Dir)
		if err != nil {
			return err
		}
		if !equal(current, base) {
			return ErrConflict
		}
		token := make([]byte, 16)
		if _, err = rand.Read(token); err != nil {
			return err
		}
		tx := transaction{ID: hex.EncodeToString(token), Base: base, Next: next, Description: strings.TrimSpace(description)}
		if tx.Description == "" {
			tx.Description = "기록 수정"
		}
		if len(tx.Description) > 2000 {
			tx.Description = string([]rune(tx.Description)[:500])
		}
		if err = r.saveTransaction(tx); err != nil {
			return err
		}
		id, err = r.finishLocked(tx)
		return err
	})
	return
}
func (r *Repository) captureExternalLocked() error {
	if _, err := r.command("status"); err != nil {
		return err
	}
	out, err := r.command("--ignore-working-copy", "log", "-r", "@", "--no-graph", "-T", `empty ++ "\n" ++ description`)
	if err != nil {
		return err
	}
	parts := strings.SplitN(string(out), "\n", 2)
	desc := ""
	if len(parts) > 1 {
		desc = strings.TrimSpace(parts[1])
	}
	if strings.TrimSpace(parts[0]) == "true" && desc == "" {
		return nil
	}
	if desc == "" {
		if _, err = r.command("describe", "-m", "직접 수정한 기록"); err != nil {
			return err
		}
	}
	_, err = r.command("new")
	return err
}
func (r *Repository) transactionPath() string {
	return filepath.Join(r.Dir, ".jj", "buku-journal.transaction.json")
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicWrite(path, stageDir string, data []byte) error {
	f, err := os.CreateTemp(stageDir, "buku-write-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func (r *Repository) saveTransaction(tx transaction) error {
	data, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return atomicWrite(r.transactionPath(), filepath.Join(r.Dir, ".jj"), data)
}
func (r *Repository) recoverLocked() error {
	data, err := os.ReadFile(r.transactionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 4*maxSnapshotSize {
		return errors.New("invalid recovery transaction")
	}
	var tx transaction
	if err = json.Unmarshal(data, &tx); err != nil {
		return err
	}
	if tx.ID == "" {
		return errors.New("invalid recovery transaction")
	}
	if err = validate(tx.Base); err != nil {
		return err
	}
	if err = validate(tx.Next); err != nil {
		return err
	}
	_, err = r.finishLocked(tx)
	return err
}
func (r *Repository) finishLocked(tx transaction) (string, error) {
	marker := "Buku-Transaction: " + tx.ID
	// A restart after jj new but before removing the manifest must not create
	// a duplicate change. Read the stored graph without snapshotting editors.
	out, err := r.command("--ignore-working-copy", "log", "-r", "ancestors(@, 100)", "--no-graph", "-T", `json({"id" => commit_id, "description" => description, "timestamp" => ""}) ++ "\n"`)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var c Change
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		if strings.Contains(c.Description, marker) {
			head, e := r.command("--ignore-working-copy", "log", "-r", "@", "--no-graph", "-T", "commit_id")
			if e != nil {
				return "", e
			}
			if strings.TrimSpace(string(head)) == c.ID {
				current, e := snapshot(r.Dir)
				if e != nil {
					return "", e
				}
				if !equal(current, tx.Next) {
					return "", ErrConflict
				}
				if _, e = r.command("new"); e != nil {
					return "", e
				}
			}
			if err = os.Remove(r.transactionPath()); err != nil {
				return "", err
			}
			return c.ID, syncDir(filepath.Join(r.Dir, ".jj"))
		}
	}
	current, err := snapshot(r.Dir)
	if err != nil {
		return "", err
	}
	keys := clone(tx.Base)
	for name, data := range tx.Next {
		keys[name] = data
	}
	// Interrupted multi-file writes are recognizable because each file must
	// still be exactly its old or new version. Unknown edits stop recovery.
	for name := range keys {
		if !sameFile(current, tx.Base, name) && !sameFile(current, tx.Next, name) {
			return "", fmt.Errorf("%w: %s", ErrConflict, name)
		}
	}
	for name := range current {
		if _, a := keys[name]; !a {
			return "", ErrConflict
		}
	}
	for _, name := range names(keys) {
		fresh, err := snapshot(r.Dir)
		if err != nil {
			return "", err
		}
		if !sameFile(fresh, current, name) {
			return "", ErrConflict
		}
		next, exists := tx.Next[name]
		if exists {
			if !sameFile(fresh, tx.Next, name) {
				if err = atomicWrite(filepath.Join(r.Dir, name), filepath.Join(r.Dir, ".jj"), next); err != nil {
					return "", err
				}
			}
		} else if _, exists = fresh[name]; exists {
			if err = os.Remove(filepath.Join(r.Dir, name)); err != nil {
				return "", err
			}
			if err = syncDir(r.Dir); err != nil {
				return "", err
			}
		}
	}
	current, err = snapshot(r.Dir)
	if err != nil {
		return "", err
	}
	if !equal(current, tx.Next) {
		return "", ErrConflict
	}
	if _, err = r.command("describe", "-m", tx.Description+"\n\n"+marker); err != nil {
		return "", err
	}
	out, err = r.command("--ignore-working-copy", "log", "-r", "@", "--no-graph", "-T", "commit_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if _, err = r.command("new"); err != nil {
		return "", err
	}
	if err = os.Remove(r.transactionPath()); err != nil {
		return "", err
	}
	return id, syncDir(filepath.Join(r.Dir, ".jj"))
}

func (r *Repository) History() (changes []Change, err error) {
	changes = []Change{}
	err = r.locked(func() error {
		if err := r.recoverLocked(); err != nil {
			return err
		}
		if err := r.captureExternalLocked(); err != nil {
			return err
		}
		out, err := r.command("--ignore-working-copy", "log", "-r", "ancestors(@) ~ root() ~ empty()", "--limit", "100", "--no-graph", "-T", `json({"id" => commit_id, "description" => description, "timestamp" => committer.timestamp().format("%Y-%m-%dT%H:%M:%S%:z")}) ++ "\n"`)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			var c Change
			if err = json.Unmarshal([]byte(line), &c); err != nil {
				return err
			}
			c.Description = strings.TrimSpace(strings.Split(c.Description, "\n\nBuku-Transaction:")[0])
			changes = append(changes, c)
		}
		return nil
	})
	return
}
func validRevision(id string) error {
	if !revisionPattern.MatchString(id) {
		return errors.New("유효하지 않은 이력 ID입니다")
	}
	return nil
}
func (r *Repository) Diff(id string) (diff string, err error) {
	if err = validRevision(id); err != nil {
		return
	}
	err = r.locked(func() error {
		out, e := r.command("--ignore-working-copy", "diff", "-r", id, "--git")
		diff = string(out)
		return e
	})
	return
}
func (r *Repository) revisionFiles(rev string) (map[string][]byte, error) {
	out, err := r.command("--ignore-working-copy", "file", "list", "-r", rev, "-T", `json(path) ++ "\n"`)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var name string
		if err = json.Unmarshal([]byte(line), &name); err != nil {
			return nil, err
		}
		if !ValidFilename(name) {
			continue
		}
		data, err := r.command("--ignore-working-copy", "file", "show", "-r", rev, "--", "file:"+strconv.Quote(name))
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

// Revert conservatively inverses a change. Unrelated later files survive;
// later edits to any affected file require the user's explicit resolution.
func (r *Repository) Revert(id string) (result string, err error) {
	if err = validRevision(id); err != nil {
		return
	}
	var base, next map[string][]byte
	err = r.locked(func() error {
		if err := r.recoverLocked(); err != nil {
			return err
		}
		if err := r.captureExternalLocked(); err != nil {
			return err
		}
		count, err := r.command("--ignore-working-copy", "log", "-r", id+" & ancestors(@)", "--no-graph", "-T", "commit_id")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(count)) == "" {
			return errors.New("현재 기록의 이력이 아닙니다")
		}
		after, err := r.revisionFiles(id)
		if err != nil {
			return err
		}
		before, err := r.revisionFiles(id + "-")
		if err != nil {
			return err
		}
		base, err = snapshot(r.Dir)
		if err != nil {
			return err
		}
		next = clone(base)
		keys := clone(before)
		for name, data := range after {
			keys[name] = data
		}
		for name := range keys {
			if sameFile(before, after, name) {
				continue
			}
			if !sameFile(base, after, name) {
				return fmt.Errorf("%w: %s", ErrConflict, name)
			}
			if data, ok := before[name]; ok {
				next[name] = data
			} else {
				delete(next, name)
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	return r.Apply(base, next, "기록 되돌리기: "+id[:12])
}
