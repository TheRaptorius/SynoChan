// Package dsm integrates with Synology DSM: the CGI front end served by DSM's web server, which checks the
// DSM login and forwards API calls to the daemon over its unix socket.
package dsm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"synochan/internal/server"
)

// Variables rather than constants so tests can point them at fakes.
var (
	// AuthenticateCGI prints the user name of a valid DSM session.
	AuthenticateCGI = "/usr/syno/synoman/webman/modules/authenticate.cgi"
	// GroupFile lists group members, used for the administrators check.
	GroupFile = "/etc/group"
)

// AdminGroup is the DSM administrators group.
const AdminGroup = "administrators"

// Authenticator returns the DSM user behind a request, or an error if there is no valid session.
type Authenticator func(r *http.Request) (string, error)

// ErrUnauthenticated means there is no valid DSM session.
var ErrUnauthenticated = errors.New("not signed in to DSM")

// ErrForbidden means the user may not use the app.
var ErrForbidden = errors.New("only DSM administrators can use SynoChan")

var validUser = regexp.MustCompile(`^[\p{L}\p{N}._@\\ -]{1,128}$`)

// SessionAuthenticator checks the DSM session by running authenticate.cgi with the request's
// cookies and SynoToken. With adminsOnly, the user must also be in the administrators group.
func SessionAuthenticator(adminsOnly bool) Authenticator {
	return func(r *http.Request) (string, error) {
		token := r.Header.Get("X-Syno-Token")
		if token == "" {
			token = r.URL.Query().Get("SynoToken")
		}
		env := make([]string, 0, len(os.Environ())+2)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "QUERY_STRING=") && !strings.HasPrefix(kv, "REQUEST_METHOD=") {
				env = append(env, kv)
			}
		}
		query := ""
		if token != "" {
			query = "SynoToken=" + url.QueryEscape(token)
		}
		env = append(env, "QUERY_STRING="+query, "REQUEST_METHOD=GET")

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, AuthenticateCGI)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrUnauthenticated, err)
		}
		user := lastLine(out)
		if user == "" || !validUser.MatchString(user) {
			return "", ErrUnauthenticated
		}
		if adminsOnly {
			ok, err := InGroup(GroupFile, AdminGroup, user)
			if err != nil {
				return "", fmt.Errorf("check group membership: %w", err)
			}
			if !ok {
				return user, ErrForbidden
			}
		}
		return user, nil
	}
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// InGroup reports whether user is a member of group according to an /etc/group style file.
func InGroup(groupFile, group, user string) (bool, error) {
	f, err := os.Open(groupFile)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), ":", 4)
		if len(fields) < 4 || fields[0] != group {
			continue
		}
		for _, member := range strings.Split(fields[3], ",") {
			if strings.EqualFold(strings.TrimSpace(member), user) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, sc.Err()
}

// Frontend is the CGI handler: it authenticates the caller and forwards "?path=<api path>" to the daemon.
type Frontend struct {
	Socket string
	Auth   Authenticator
	proxy  *httputil.ReverseProxy
}

// NewFrontend creates the CGI handler for a daemon listening on socket.
func NewFrontend(socket string, auth Authenticator) *Frontend {
	f := &Frontend{Socket: socket, Auth: auth}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	f.proxy = &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "synochan"
			pr.Out.Host = "synochan"
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSONError(w, http.StatusServiceUnavailable, "The SynoChan service is not running. Start it in Package Center.")
		},
	}
	return f
}

var apiPath = regexp.MustCompile(`^[a-z]+(/[a-z]+)?$`)

func (f *Frontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := q.Get("path")
	if !apiPath.MatchString(p) {
		writeJSONError(w, http.StatusNotFound, "unknown API path")
		return
	}

	// Browser extension calls carry an API token instead of a DSM session. The daemon checks the token and
	// accepts it only on the ext/ routes, so nothing else is reachable this way.
	if strings.HasPrefix(p, "ext/") {
		if r.Header.Get(server.TokenHeader) == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing API token")
			return
		}
		f.forward(w, r, p, q, "")
		return
	}

	user, err := f.Auth(r)
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			writeJSONError(w, http.StatusForbidden, ErrForbidden.Error())
		case r.Header.Get("X-Syno-Token") == "" && r.URL.Query().Get("SynoToken") == "":
			writeJSONError(w, http.StatusUnauthorized, "No DSM session was passed. Open SynoChan from the DSM main menu.")
		default:
			writeJSONError(w, http.StatusUnauthorized, "Your DSM session has expired. Sign in to DSM again and reopen SynoChan.")
		}
		return
	}

	f.forward(w, r, p, q, user)
}

func (f *Frontend) forward(w http.ResponseWriter, r *http.Request, p string, q url.Values, user string) {
	q.Del("path")
	q.Del("SynoToken")
	out := r.Clone(r.Context())
	out.URL = &url.URL{Path: "/api/" + p, RawQuery: q.Encode()}
	out.RequestURI = ""
	out.Header.Del("Cookie") // The daemon has no use for the DSM session cookie.
	out.Header.Del("X-Syno-Token")
	out.Header.Del(server.UserHeader)
	if user != "" {
		out.Header.Set(server.UserHeader, user)
	}
	if !strings.HasPrefix(p, "ext/") {
		out.Header.Del(server.TokenHeader)
	}
	f.proxy.ServeHTTP(w, out)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"error": msg})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}
