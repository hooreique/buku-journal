package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"buku-journal/internal/journal"
	"buku-journal/internal/server"
	"buku-journal/internal/worker"
)

func xdg(key, fallback string) string {
	if s := os.Getenv(key); filepath.IsAbs(s) {
		return s
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}
func config(path string) (map[string]string, error) {
	out := map[string]string{}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	known := map[string]bool{"journal_dir": true, "state_dir": true, "listen": true, "timezone": true, "codex": true, "jj": true, "model": true, "token_file": true, "worker_timeout": true}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !known[key] {
			return nil, fmt.Errorf("config line %d: unknown setting", i+1)
		}
		v, e := strconv.Unquote(strings.TrimSpace(value))
		if e != nil {
			return nil, fmt.Errorf("config line %d: expected a quoted string", i+1)
		}
		out[key] = v
	}
	return out, nil
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println("buku-journal 0.1.0")
		return nil
	}
	command := "serve"
	if len(args) > 0 && (args[0] == "serve" || args[0] == "import") {
		command = args[0]
		args = args[1:]
	}
	configFile := filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "buku-journal", "config.toml")
	cfg, e := config(configFile)
	if e != nil {
		return e
	}
	get := func(k, def string) string {
		if v, ok := cfg[k]; ok {
			return v
		}
		return def
	}
	dataDir := filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "buku-journal")
	f := flag.NewFlagSet("buku-journal "+command, flag.ContinueOnError)
	listen := f.String("listen", get("listen", "127.0.0.1:3002"), "HTTP listen address")
	repoDir := f.String("journal-dir", get("journal_dir", filepath.Join(dataDir, "journal")), "Journal jj repository")
	stateDir := f.String("state-dir", get("state_dir", filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "buku-journal")), "Durable execution state directory")
	tokenFile := f.String("token-file", get("token_file", filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "buku-journal", "token")), "Private access key file (created if absent)")
	codex := f.String("codex", get("codex", "codex"), "Codex CLI executable")
	jj := f.String("jj", get("jj", "jj"), "Jujutsu executable")
	model := f.String("model", get("model", ""), "Optional Codex model override")
	zone := f.String("timezone", get("timezone", "Asia/Seoul"), "Journal time zone")
	timeoutDefault, e := time.ParseDuration(get("worker_timeout", "5m"))
	if e != nil {
		return e
	}
	timeout := f.Duration("worker-timeout", timeoutDefault, "Maximum Codex execution time")
	from := f.String("from", "", "Source directory for one-time import")
	if e = f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *timeout < time.Second {
		return errors.New("worker timeout must be at least one second")
	}
	location, e := time.LoadLocation(*zone)
	if e != nil {
		return e
	}
	for _, p := range []*string{repoDir, stateDir, tokenFile} {
		abs, e := filepath.Abs(*p)
		if e != nil {
			return e
		}
		*p = abs
	}
	if e = os.MkdirAll(*stateDir, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(*stateDir, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("another buku-journal process is already using this state directory")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	repo, e := journal.Open(*repoDir, *jj)
	if e != nil {
		return e
	}
	if command == "import" {
		if *from == "" {
			return errors.New("import requires --from DIR")
		}
		base, e := repo.Snapshot()
		if e != nil {
			return e
		}
		if len(base) > 0 {
			return errors.New("import target already contains records; refusing to overwrite")
		}
		entries, e := os.ReadDir(*from)
		if e != nil {
			return e
		}
		next := map[string][]byte{}
		for _, entry := range entries {
			if !journal.ValidFilename(entry.Name()) {
				continue
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("not a regular file: %s", entry.Name())
			}
			b, e := os.ReadFile(filepath.Join(*from, entry.Name()))
			if e != nil {
				return e
			}
			next[entry.Name()] = b
		}
		if len(next) == 0 {
			return errors.New("no journal records to import")
		}
		id, e := repo.Apply(base, next, "기존 기록 가져오기")
		if e != nil {
			return e
		}
		fmt.Printf("Imported %d files into %s (change %s)\n", len(next), repo.Dir, id)
		return nil
	}
	token, e := server.Token(*tokenFile)
	if e != nil {
		return e
	}
	jobs, e := worker.Open(repo, *stateDir, filepath.Join(filepath.Dir(*repoDir), "inbox"), *codex, *model, *timeout, location)
	if e != nil {
		return e
	}
	defer jobs.Close()
	handler := server.New(repo, jobs, token)
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	done := make(chan error, 1)
	go func() {
		log.Printf("buku-journal listening on %s; journal %s", *listen, *repoDir)
		done <- srv.ListenAndServe()
	}()
	select {
	case e = <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
