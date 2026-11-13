// Command synochan is a 4chan thread and board archiver for Synology DSM.
//
// Modes:
//
//	synochan daemon [flags]   run the background service
//	synochan cgi              CGI front end, run by DSM's web server (also chosen when GATEWAY_INTERFACE is set)
//	synochan version          print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"synochan/internal/config"
	"synochan/internal/core"
	"synochan/internal/dsm"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
	"synochan/internal/logx"
	"synochan/internal/server"
	"synochan/web"
)

// Set at build time with -ldflags "-X main.version=… -X main.repoURL=… -X main.author=…".
var (
	version = "dev"
	repoURL = "https://github.com/TheRaptorius/SynoChan"
	author  = "TheRaptorius"
)

const (
	defaultDataDir = "/var/packages/synochan/var"
	socketName     = "synochan.sock"
)

func main() {
	if os.Getenv("GATEWAY_INTERFACE") != "" {
		os.Exit(runCGI())
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon":
		os.Exit(runDaemon(os.Args[2:]))
	case "cgi":
		os.Exit(runCGI())
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: synochan daemon [flags] | synochan cgi | synochan version")
}

func runCGI() int {
	socket := os.Getenv("SYNOCHAN_SOCKET")
	if socket == "" {
		socket = filepath.Join(defaultDataDir, socketName)
	}
	auth := dsm.SessionAuthenticator(true)
	if os.Getenv("SYNOCHAN_INSECURE_NO_AUTH") == "1" {
		auth = func(*http.Request) (string, error) { return "dev", nil }
	}
	if err := cgi.Serve(dsm.NewFrontend(socket, auth)); err != nil {
		fmt.Fprintln(os.Stderr, "cgi:", err)
		return 1
	}
	return 0
}

func runDaemon(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	dataDir := fs.String("data", defaultDataDir, "directory for settings, saved lists, log and socket")
	socket := fs.String("socket", "", "unix socket path (default <data>/"+socketName+")")
	listen := fs.String("listen", "", "also serve the UI and API without authentication on this TCP address, e.g. 127.0.0.1:8580")
	defaultSave := fs.String("default-save-path", "", "save folder used on first start")
	endpointsBase := fs.String("endpoints-base", "", "testing: use this base URL for all image board hosts")
	logStderr := fs.Bool("log-stderr", false, "also log to stderr")
	debug := fs.Bool("debug", false, "verbose logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" {
		*socket = filepath.Join(*dataDir, socketName)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "create data dir:", err)
		return 1
	}

	logPath := filepath.Join(*dataDir, "synochan.log")
	logFile, err := logx.Open(logPath, 2<<20)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open log:", err)
		return 1
	}
	defer logFile.Close()
	var logOut io.Writer = logFile
	if *logStderr {
		logOut = io.MultiWriter(logFile, os.Stderr)
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	savePath := *defaultSave
	if savePath == "" {
		savePath = filepath.Join(*dataDir, "downloads")
	}
	if resolved, err := filepath.EvalSymlinks(savePath); err == nil {
		savePath = resolved // DSM exposes shares as /var/packages/<pkg>/shares/<name> links; show the real path.
	}
	cfg, err := config.Load(filepath.Join(*dataDir, "settings.json"), config.Defaults(savePath))
	if err != nil {
		log.Error("could not load settings", "err", err)
		return 1
	}

	endpoints := fourchan.DefaultEndpoints
	if *endpointsBase != "" {
		b := strings.TrimRight(*endpointsBase, "/")
		endpoints = fourchan.Endpoints{API: b, Images: b, Thumbs: b, Boards: b}
	}

	client := fetch.New(func() string { return cfg.Get().UserAgent })
	m := core.New(core.Options{
		Config:    cfg,
		Client:    client,
		Endpoints: endpoints,
		StatePath: filepath.Join(*dataDir, "state.json"),
		Logger:    log,
	})
	if err := m.Start(); err != nil {
		log.Error("started with errors", "err", err)
	}
	log.Info("SynoChan started", "version", version, "data", *dataDir, "save_path", cfg.Get().SavePath)

	api := server.New(m, server.Info{Version: version, RepoURL: repoURL, Author: author, LogPath: logPath, DataDir: *dataDir})

	os.Remove(*socket)
	unixLn, err := net.Listen("unix", *socket)
	if err != nil {
		log.Error("could not listen on socket", "socket", *socket, "err", err)
		return 1
	}
	// The CGI front end may run as a different user than the daemon; access is controlled by its DSM login check.
	os.Chmod(*socket, 0o666)

	servers := []*http.Server{{Handler: api, ReadHeaderTimeout: 10 * time.Second}}
	listeners := []net.Listener{unixLn}

	if *listen != "" {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Error("could not listen", "addr", *listen, "err", err)
			return 1
		}
		log.Warn("serving UI without authentication", "addr", *listen)
		servers = append(servers, &http.Server{Handler: standalone(api), ReadHeaderTimeout: 10 * time.Second})
		listeners = append(listeners, ln)
	}

	errc := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { errc <- srv.Serve(listeners[i]) }()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case s := <-sig:
		log.Info("stopping", "signal", s.String())
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		srv.Shutdown(ctx)
	}
	m.Shutdown(20 * time.Second)
	os.Remove(*socket)
	log.Info("SynoChan stopped")
	return 0
}

// standalone serves the embedded UI and maps the CGI style "api.cgi?path=x" calls to "/api/x",
// so the same UI works outside DSM.
func standalone(api http.Handler) http.Handler {
	static := http.FileServerFS(web.Static())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api.cgi":
			q := r.URL.Query()
			p := q.Get("path")
			q.Del("path")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/api/" + p
			r2.URL.RawQuery = q.Encode()
			r2.Header.Set(server.UserHeader, "standalone")
			api.ServeHTTP(w, r2)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			api.ServeHTTP(w, r)
		default:
			static.ServeHTTP(w, r)
		}
	})
}
