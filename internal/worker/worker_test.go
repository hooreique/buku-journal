package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"buku-journal/internal/journal"
)

const testEntry = "2026-10-23-산책.md"

type fixture struct {
	repo         *journal.Repository
	state, inbox string
	manager      *Manager
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!" + shell + "\nset -eu\nout=''\nwhile [ $# -gt 0 ]; do\n case \"$1\" in -o) shift; out=$1 ;; esac\n shift\ndone\ninput=$(cat)\n" + body + "\n"
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func setup(t *testing.T, script string) *fixture {
	t.Helper()
	jj, err := exec.LookPath("jj")
	if err != nil {
		t.Skip("jj not installed")
	}
	root := t.TempDir()
	repo, err := journal.Open(filepath.Join(root, "journal"), jj)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Apply(map[string][]byte{}, map[string][]byte{testEntry: []byte("산책했음\n")}, "기존 기록"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{repo: repo, state: filepath.Join(root, "state"), inbox: filepath.Join(root, "inbox")}
	f.manager, err = Open(repo, f.state, f.inbox, fakeCodex(t, script), "", 8*time.Second, time.FixedZone("Asia/Seoul", 9*3600))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.manager.Close() })
	return f
}

// Get provides a snapshot lookup for worker assertions.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return clone(j), true
}

func waitJob(t *testing.T, m *Manager, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := m.Get(id)
		if !ok {
			t.Fatal("job disappeared")
		}
		if job.Status == status {
			return job
		}
		if job.Status == "failed" && status != "failed" {
			t.Fatalf("job failed: %s", job.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, _ := m.Get(id)
	t.Fatalf("wanted %s, got %#v", status, job)
	return Job{}
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(path); e == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file was never created: %s", path)
}
func TestQueueAndIdempotentSubmission(t *testing.T) {
	f := setup(t, `case "$input" in
 *second*) printf '둘째 기록\n' > '2026-10-25-둘째.md' ;;
 *) printf '첫째 기록\n' > '2026-10-24-첫째.md'; : > '빈장소.txt' ;;
esac
printf '%s' '{"status":"ready","summary":"기록했어요.","questions":[]}' > "$out"`)
	one, err := f.manager.Submit("first", "", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.manager.Submit("first", "", "request-1")
	if err != nil || again.ID != one.ID {
		t.Fatalf("not idempotent: %#v %v", again, err)
	}
	if _, err = f.manager.Submit("different", "", "request-1"); err == nil {
		t.Fatal("accepted changed duplicate request")
	}
	two, err := f.manager.Submit("second", "", "request-2")
	if err != nil {
		t.Fatal(err)
	}
	complete := waitJob(t, f.manager, one.ID, "completed")
	waitJob(t, f.manager, two.ID, "completed")
	if len(complete.ChangedFiles) != 2 || complete.ChangeID == "" {
		t.Fatalf("missing commit metadata: %#v", complete)
	}
	actual, err := f.repo.Snapshot()
	if err != nil || len(actual) != 4 {
		t.Fatalf("queue lost files: %#v %v", actual, err)
	}
	history, err := f.repo.History()
	if err != nil || len(history) != 3 {
		t.Fatalf("history %#v %v", history, err)
	}
	if _, err = os.Stat(filepath.Join(f.inbox, one.ID+".json")); err != nil {
		t.Fatal("input was not preserved")
	}
}
func TestClarificationAndAnswer(t *testing.T) {
	f := setup(t, `case "$input" in
 *clarified*) printf '날짜를 확인하고 만남\n' > '2026-10-24-만남.md'; printf '%s' '{"status":"ready","summary":"날짜를 확인했어요.","questions":[]}' > "$out" ;;
 *) printf '%s' '{"status":"needs_input","summary":"날짜가 필요해요.","questions":["언제였나요?"]}' > "$out" ;;
esac`)
	job, err := f.manager.Submit("그날 만났어", "", "clarify")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitJob(t, f.manager, job.ID, "needs_input")
	if len(pending.Questions) != 1 {
		t.Fatal("missing question")
	}
	entries, _, err := f.repo.List()
	if err != nil || len(entries) != 1 {
		t.Fatal("clarification modified journal")
	}
	if _, err = f.manager.Answer(job.ID, "2026-10-24 clarified"); err != nil {
		t.Fatal(err)
	}
	complete := waitJob(t, f.manager, job.ID, "completed")
	if len(complete.Answers) != 1 || len(complete.Questions) != 0 {
		t.Fatalf("answer not retained: %#v", complete)
	}
}
func TestRejectsInvalidWorkerChanges(t *testing.T) {
	cases := map[string]string{
		"missing-link": `printf '[카페](./없는곳.txt) 방문\n' > '2026-10-24-카페.md'; printf '%s' '{"status":"ready","summary":"완료","questions":[]}' > "$out"`,
		"malformed":    "printf 'not-json' > \"$out\"",
		"delete":       `rm '2026-10-23-산책.md'; printf '%s' '{"status":"ready","summary":"삭제","questions":[]}' > "$out"`,
		"extra-file":   `printf bad > 'script.sh'; printf '%s' '{"status":"ready","summary":"완료","questions":[]}' > "$out"`,
		"symlink":      `ln -s '2026-10-23-산책.md' '2026-10-24-link.md'; printf '%s' '{"status":"ready","summary":"완료","questions":[]}' > "$out"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t, body)
			job, err := f.manager.Submit("기록해 줘", "", "invalid")
			if err != nil {
				t.Fatal(err)
			}
			failed := waitJob(t, f.manager, job.ID, "failed")
			if failed.Error == "" {
				t.Fatal("missing error")
			}
			actual, err := f.repo.Snapshot()
			if err != nil || len(actual) != 1 || string(actual[testEntry]) != "산책했음\n" {
				t.Fatalf("invalid result changed data: %#v %v", actual, err)
			}
		})
	}
}
func TestCancelKillsRunningWorker(t *testing.T) {
	control := t.TempDir()
	started := filepath.Join(control, "started")
	f := setup(t, "touch "+shellQuote(started)+"\nsleep 30\nprintf '%s' '{\"status\":\"ready\",\"summary\":\"완료\",\"questions\":[]}' > \"$out\"")
	job, err := f.manager.Submit("오래 걸리는 기록", "", "cancel")
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)
	if _, err = f.manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.manager, job.ID, "cancelled")
	f.manager.Close() // must not wait for the fake worker's 30-second sleep
	actual, err := f.repo.Snapshot()
	if err != nil || len(actual) != 1 {
		t.Fatalf("cancel changed data: %#v %v", actual, err)
	}
}
func TestConcurrentManualEditIsPreserved(t *testing.T) {
	control := t.TempDir()
	started := filepath.Join(control, "started")
	release := filepath.Join(control, "release")
	script := "touch " + shellQuote(started) + "\nwhile [ ! -f " + shellQuote(release) + " ]; do sleep 0.02; done\nprintf 'worker result\\n' > '2026-10-23-산책.md'\nprintf '%s' '{\"status\":\"ready\",\"summary\":\"수정\",\"questions\":[]}' > \"$out\""
	f := setup(t, script)
	job, err := f.manager.Submit("내용 보완", testEntry, "race")
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, started)
	if err = os.WriteFile(filepath.Join(f.repo.Dir, testEntry), []byte("editor result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.manager, job.ID, "failed")
	actual, err := f.repo.Snapshot()
	if err != nil || string(actual[testEntry]) != "editor result\n" {
		t.Fatalf("editor lost: %#v %v", actual, err)
	}
}
func TestRestartReconcilesCommittedJob(t *testing.T) {
	f := setup(t, `printf '정리했음\n' > '2026-10-24-기록.md'; printf '%s' '{"status":"ready","summary":"추가했어요.","questions":[]}' > "$out"`)
	submitted, err := f.manager.Submit("정리해 줘", "", "restart")
	if err != nil {
		t.Fatal(err)
	}
	complete := waitJob(t, f.manager, submitted.ID, "completed")
	f.manager.Close()
	complete.Status = "saving"
	complete.ChangeID = ""
	data, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.state, "jobs", complete.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	next, err := Open(f.repo, f.state, f.inbox, "/does/not/exist", "", time.Second, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	reconciled, ok := next.Get(complete.ID)
	if !ok || reconciled.Status != "completed" || reconciled.ChangeID == "" {
		t.Fatalf("not reconciled: %#v", reconciled)
	}
	changes, err := f.repo.History()
	if err != nil || len(changes) != 2 {
		t.Fatalf("duplicate changes %#v %v", changes, err)
	}
}

// These persistence regressions use a directory at the destination filename
// to force an atomic rename failure, even when tests run with elevated access.
func persistenceManager(t *testing.T, status string) (*Manager, *Job) {
	t.Helper()
	root := t.TempDir()
	m := &Manager{state: filepath.Join(root, "state"), inbox: filepath.Join(root, "inbox"), jobs: map[string]*Job{}, wake: make(chan struct{}, 1)}
	for _, dir := range []string{filepath.Join(m.state, "jobs"), m.inbox} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	j := &Job{ID: NewID(), Status: status, Message: "원래 입력", Questions: []string{"언제였나요?"}, Answers: []string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := m.save(j); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(m.inbox, j.ID+".json"), j); err != nil {
		t.Fatal(err)
	}
	m.jobs[j.ID] = j
	return m, j
}

func blockPersistence(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestAnswerPersistenceFailureKeepsPendingQuestion(t *testing.T) {
	for _, destination := range []string{"inbox", "state"} {
		t.Run(destination, func(t *testing.T) {
			m, j := persistenceManager(t, "needs_input")
			path := filepath.Join(m.inbox, j.ID+".json")
			if destination == "state" {
				path = filepath.Join(m.state, "jobs", j.ID+".json")
			}
			blockPersistence(t, path)
			if _, err := m.Answer(j.ID, "어제였어"); err == nil {
				t.Fatal("answer reported success despite failed persistence")
			}
			current, _ := m.Get(j.ID)
			if current.Status != "needs_input" || len(current.Answers) != 0 || len(current.Questions) != 1 {
				t.Fatalf("failed answer changed live job: %#v", current)
			}
			select {
			case <-m.wake:
				t.Fatal("failed answer scheduled a worker")
			default:
			}
		})
	}
}

func TestCancelPersistenceFailureDoesNotCancelExecution(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		t.Run(status, func(t *testing.T) {
			m, j := persistenceManager(t, status)
			cancelled := false
			m.activeID = j.ID
			m.activeCancel = func() { cancelled = true }
			blockPersistence(t, filepath.Join(m.state, "jobs", j.ID+".json"))
			if _, err := m.Cancel(j.ID); err == nil {
				t.Fatal("cancel reported success despite failed persistence")
			}
			current, _ := m.Get(j.ID)
			if current.Status != status || cancelled {
				t.Fatalf("failed cancellation changed job/execution: %#v cancelled=%v", current, cancelled)
			}
		})
	}
}

func TestReadResultFileRejectsUnsafeFilesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("valid"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readResultFile(regular, 5); err != nil || string(data) != "valid" {
		t.Fatalf("regular result rejected: %q %v", data, err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"fifo": fifo, "symlink": symlink, "oversized": regular, "directory": dir} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { _, err := readResultFile(path, 4); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("accepted unsafe result %s", name)
				}
			case <-time.After(time.Second):
				t.Fatalf("blocked while reading %s", name)
			}
		})
	}
}

func retryManager(t *testing.T, status string) (*Manager, *Job) {
	t.Helper()
	m, j := persistenceManager(t, status)
	jj, err := exec.LookPath("jj")
	if err != nil {
		t.Skip("jj not installed")
	}
	m.repo, err = journal.Open(filepath.Join(t.TempDir(), "journal"), jj)
	if err != nil {
		t.Fatal(err)
	}
	return m, j
}
func TestRetryPreservesOriginalTimeAndClarifications(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			m, j := retryManager(t, status)
			j.CreatedAt = "2026-10-30T15:01:02Z"
			j.Message = "어제 만났어"
			j.EntryID = testEntry
			j.Answers = []string{"성수에서 만났어", "10월 30일이 맞아"}
			j.Questions = []string{"어디에서 만났나요?"}
			j.RequestID = "original-request"
			j.Error = "previous failure"
			got, err := m.Retry(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != j.ID || got.CreatedAt != j.CreatedAt || got.Message != j.Message || got.EntryID != j.EntryID || got.RequestID != j.RequestID || len(got.Answers) != 2 || got.Answers[1] != j.Answers[1] || len(got.Questions) != 1 || got.Status != "queued" || got.Error != "" {
				t.Fatalf("retry lost original context: %#v", got)
			}
			data, err := os.ReadFile(filepath.Join(m.state, "jobs", j.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved Job
			if err = json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.CreatedAt != j.CreatedAt || len(saved.Answers) != 2 || saved.Status != "queued" {
				t.Fatalf("context was not persisted: %#v", saved)
			}
			select {
			case <-m.wake:
			default:
				t.Fatal("retry was not enqueued")
			}
			again, err := m.Retry(j.ID)
			if err != nil || again.ID != got.ID || again.UpdatedAt != got.UpdatedAt {
				t.Fatalf("retry is not idempotent: %#v %v", again, err)
			}
			select {
			case <-m.wake:
				t.Fatal("duplicate retry enqueued another run")
			default:
			}
		})
	}
}
func TestRetryPersistenceFailureDoesNotEnqueue(t *testing.T) {
	m, j := retryManager(t, "failed")
	blockPersistence(t, filepath.Join(m.state, "jobs", j.ID+".json"))
	if _, err := m.Retry(j.ID); err == nil {
		t.Fatal("retry succeeded despite persistence failure")
	}
	current, _ := m.Get(j.ID)
	if current.Status != "failed" {
		t.Fatalf("failed save changed live status: %#v", current)
	}
	select {
	case <-m.wake:
		t.Fatal("failed retry enqueued")
	default:
	}
}
func TestRetryReconcilesAlreadyCommittedChange(t *testing.T) {
	m, j := retryManager(t, "failed")
	commit, err := m.repo.Apply(map[string][]byte{}, map[string][]byte{testEntry: []byte("이미 기록했음\n")}, "기록 정리\n\n[job:"+j.ID+"]")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Retry(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.ChangeID != commit {
		t.Fatalf("committed retry was not reconciled: %#v", got)
	}
	select {
	case <-m.wake:
		t.Fatal("committed job was run twice")
	default:
	}
	again, err := m.Retry(j.ID)
	if err != nil || again.Status != "completed" || again.ChangeID != commit {
		t.Fatalf("completed retry failed: %#v %v", again, err)
	}
	history, err := m.repo.History()
	if err != nil || len(history) != 1 {
		t.Fatalf("retry duplicated history: %#v %v", history, err)
	}
}
func TestRetryWaitsForCancelledProcessExit(t *testing.T) {
	m, j := retryManager(t, "cancelled")
	m.activeID = j.ID
	if _, err := m.Retry(j.ID); err == nil {
		t.Fatal("retried before old process finished")
	}
	current, _ := m.Get(j.ID)
	if current.Status != "cancelled" {
		t.Fatal("active cancellation status changed")
	}
	select {
	case <-m.wake:
		t.Fatal("retry enqueued before process exit")
	default:
	}
}

func TestWorkerUsesConfiguredTimezone(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "input")
	f := setup(t, "printf '%s' \"$input\" > "+shellQuote(capture)+`
printf '%s' '{"status":"ready","summary":"확인했어요.","questions":[]}' > "$out"`)
	f.manager.Close()
	f.manager.location = time.FixedZone("America/New_York", -4*60*60)
	job, err := f.manager.Submit("어제 만났어", "", "timezone")
	if err != nil {
		t.Fatal(err)
	}
	job.CreatedAt = "2026-11-01T01:30:00Z"
	f.manager.run(context.Background(), job)
	waitJob(t, f.manager, job.ID, "completed")
	input, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, ok := strings.Cut(string(input), "USER DATA (JSON):\n")
	var data struct{ SubmittedAt, Timezone string }
	if !ok || json.Unmarshal([]byte(payload), &data) != nil {
		t.Fatalf("missing submission context: %s", input)
	}
	if data.SubmittedAt != "2026-10-31T21:30:00-04:00" || data.Timezone != "America/New_York" {
		t.Fatalf("wrong date or timezone: %#v", data)
	}
	if strings.Contains(string(input), "Asia/Seoul") {
		t.Fatal("worker still instructs using Seoul time")
	}
}
