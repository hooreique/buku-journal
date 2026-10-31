package journal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Repository {
	t.Helper()
	jj, err := exec.LookPath("jj")
	if err != nil {
		t.Skip("jj not installed")
	}
	r, err := Open(filepath.Join(t.TempDir(), "journal"), jj)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestApplyHistoryAndExternalEdit(t *testing.T) {
	r := openTest(t)
	entry := "2026-10-23-올림픽공원.md"
	original := map[string][]byte{entry: []byte("[카페](./카페.txt) 에서 만남\n좋았음\n"), "카페.txt": []byte("카페\n서울\nhttps://example.com\n")}
	id, err := r.Apply(map[string][]byte{}, original, "기록 추가")
	if err != nil {
		t.Fatal(err)
	}
	if !revisionPattern.MatchString(id) {
		t.Fatalf("invalid id: %q", id)
	}
	entries, revision, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Date != "2026-10-23" || entries[0].Body != string(original[entry]) || revision == "" {
		t.Fatalf("entries %#v revision %s", entries, revision)
	}
	place, err := r.Place("카페.txt")
	if err != nil || place.Name != "카페" || place.URL != "https://example.com" {
		t.Fatalf("place %#v %v", place, err)
	}
	history, err := r.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != id || history[0].Description != "기록 추가" {
		t.Fatalf("history %#v", history)
	}
	diff, err := r.Diff(id)
	if err != nil || !strings.Contains(diff, "좋았음") {
		t.Fatalf("diff %s, %v", diff, err)
	}
	// An editor writes directly. Reads immediately reflect it and a stale job
	// must fail without replacing the newly supplied text.
	changed := []byte("직접 에디터로 고침\n")
	if err = os.WriteFile(filepath.Join(r.Dir, entry), changed, 0600); err != nil {
		t.Fatal(err)
	}
	next := clone(original)
	next[entry] = []byte("오래된 작업 결과\n")
	if _, err = r.Apply(original, next, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wanted conflict, got %v", err)
	}
	latest, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(latest[entry]) != string(changed) {
		t.Fatal("editor change lost")
	}
	next = clone(latest)
	next["2026-10-24-다음날.md"] = []byte("다시 만남\n")
	if _, err = r.Apply(latest, next, "다음날 추가"); err != nil {
		t.Fatal(err)
	}
	history, err = r.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[1].Description != "직접 수정한 기록" {
		t.Fatalf("manual edit not separated: %#v", history)
	}
}
func TestRevertKeepsLaterUnrelatedFiles(t *testing.T) {
	r := openTest(t)
	first := map[string][]byte{"2026-10-23-하루.md": []byte("처음\n")}
	id, err := r.Apply(map[string][]byte{}, first, "처음")
	if err != nil {
		t.Fatal(err)
	}
	second := clone(first)
	second["2026-10-24-다음날.md"] = []byte("다음\n")
	if _, err = r.Apply(first, second, "다음"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Revert(id); err != nil {
		t.Fatal(err)
	}
	actual, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != 1 || string(actual["2026-10-24-다음날.md"]) != "다음\n" {
		t.Fatalf("wrong inverse: %#v", actual)
	}
}
func TestRevertConflictingLaterEdit(t *testing.T) {
	r := openTest(t)
	first := map[string][]byte{"2026-10-23-하루.md": []byte("처음\n")}
	id, err := r.Apply(map[string][]byte{}, first, "처음")
	if err != nil {
		t.Fatal(err)
	}
	second := clone(first)
	second["2026-10-23-하루.md"] = []byte("수정\n")
	if _, err = r.Apply(first, second, "수정"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Revert(id); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	actual, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(actual, second) {
		t.Fatal("revert overwrote later edit")
	}
}
func TestUnsafeInputAndSymlink(t *testing.T) {
	r := openTest(t)
	for _, name := range []string{"../secret.txt", "/tmp/file.txt", "foo\\bar.txt", ".hidden.txt", "2026-02-30-invalid.md", "AGENTS.md", "evil\n.txt"} {
		if ValidFilename(name) {
			t.Errorf("accepted invalid %q", name)
		}
		if _, err := r.Apply(map[string][]byte{}, map[string][]byte{name: []byte("oops")}, "bad"); !errors.Is(err, ErrInvalid) {
			t.Errorf("expected invalid %q: %v", name, err)
		}
	}
	target := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(r.Dir, "place.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := r.Diff("@ | root()"); err == nil {
		t.Fatal("accepted revision expression")
	}
}
func TestRecoveryFinishesPartialWriteOnce(t *testing.T) {
	r := openTest(t)
	base := map[string][]byte{"2026-10-23-하루.md": []byte("처음\n"), "카페.txt": []byte("카페\n")}
	if _, err := r.Apply(map[string][]byte{}, base, "처음"); err != nil {
		t.Fatal(err)
	}
	next := clone(base)
	next["2026-10-23-하루.md"] = []byte("추가\n")
	next["카페.txt"] = []byte("카페\n서울\n")
	tx := transaction{ID: "test-recovery", Base: base, Next: next, Description: "복구된 작업"}
	if err := r.saveTransaction(tx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "카페.txt"), next["카페.txt"], 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.Dir, r.jjPath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reopened.Snapshot()
	if err != nil || !equal(actual, next) {
		t.Fatalf("partial recovery %#v %v", actual, err)
	}
	history, err := reopened.History()
	if err != nil || len(history) != 2 {
		t.Fatalf("history %#v %v", history, err)
	}
	// Simulate interruption after jj new but before clearing the manifest.
	if err := reopened.saveTransaction(tx); err != nil {
		t.Fatal(err)
	}
	again, err := Open(r.Dir, r.jjPath)
	if err != nil {
		t.Fatal(err)
	}
	history, err = again.History()
	if err != nil || len(history) != 2 {
		t.Fatalf("duplicate recovery %#v %v", history, err)
	}
}
func TestRecoveryDoesNotOverwriteUnrelatedEdit(t *testing.T) {
	r := openTest(t)
	base := map[string][]byte{"2026-10-23-하루.md": []byte("처음\n")}
	if _, err := r.Apply(map[string][]byte{}, base, "처음"); err != nil {
		t.Fatal(err)
	}
	next := clone(base)
	next["2026-10-23-하루.md"] = []byte("worker\n")
	if err := r.saveTransaction(transaction{ID: "conflict", Base: base, Next: next, Description: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "2026-10-23-하루.md"), []byte("editor\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(r.Dir, r.jjPath); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(r.Dir, "2026-10-23-하루.md"))
	if err != nil || string(body) != "editor\n" {
		t.Fatal("conflicting edit lost")
	}
}
func TestFilenameFilesetEscaping(t *testing.T) {
	r := openTest(t)
	first := map[string][]byte{"2026-10-23-[함께] 본 날.md": []byte("안녕\n")}
	id, err := r.Apply(map[string][]byte{}, first, "추가")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Revert(id); err != nil {
		t.Fatal(err)
	}
}
