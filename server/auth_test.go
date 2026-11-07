package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	convertSettings "gocsvify/settings"
)

const testToken = "s3cret-token"

// The tests must not depend on the developer's shell environment.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("GOCSVIFY_AUTH_TOKEN")
	os.Exit(m.Run())
}

func withToken(token string) func(*convertSettings.Settings) {
	return func(s *convertSettings.Settings) { s.AuthToken = token }
}

func (e *env) do(t *testing.T, method, path, authorization string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// A request that passes authorization reaches the handler, which answers 400 for the missing key.
func requireReachedHandler(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected the handler to answer 400, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func requireUnauthorized(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=UTF-8" {
		t.Errorf("Content-Type = %q", got)
	}
	r := decode(t, resp)
	if r.Success || r.Message != "Unauthorized" {
		t.Errorf("body = %+v", r)
	}
}

func TestAuthDisabledWithoutToken(t *testing.T) {
	e := setup(t, nil)
	requireReachedHandler(t, e.do(t, "GET", "/check", ""))
	// A header sent by a client is simply ignored.
	requireReachedHandler(t, e.do(t, "GET", "/check", "Bearer whatever"))
}

func TestAuthRejectsBadCredentials(t *testing.T) {
	e := setup(t, withToken(testToken))

	for name, header := range map[string]string{
		"missing":             "",
		"wrong token":         "Bearer wrong",
		"prefix of token":     "Bearer " + testToken[:5],
		"token with suffix":   "Bearer " + testToken + "x",
		"different case":      "Bearer " + strings.ToUpper(testToken),
		"scheme only":         "Bearer",
		"scheme and space":    "Bearer ",
		"basic scheme":        "Basic " + testToken,
		"no scheme":           testToken,
		"extra word":          "Bearer " + testToken + " extra",
		"token without space": "Bearer" + testToken,
	} {
		t.Run(name, func(t *testing.T) {
			requireUnauthorized(t, e.do(t, "GET", "/check", header))
		})
	}
}

func TestAuthAcceptsValidToken(t *testing.T) {
	e := setup(t, withToken(testToken))

	for name, header := range map[string]string{
		"canonical":      "Bearer " + testToken,
		"lowercase":      "bearer " + testToken,
		"uppercase":      "BEARER " + testToken,
		"surrounding ws": "  Bearer " + testToken + "  ",
		"extra spaces":   "Bearer   " + testToken,
	} {
		t.Run(name, func(t *testing.T) {
			requireReachedHandler(t, e.do(t, "GET", "/check", header))
		})
	}
}

func TestAuthProtectsAllEndpoints(t *testing.T) {
	e := setup(t, withToken(testToken))

	for _, tc := range []struct{ method, path string }{
		{"POST", "/upload"},
		{"GET", "/check?key=k"},
		{"GET", "/download?key=k"},
		{"GET", "/download?key=k&sheetNum=0"},
		{"GET", "/unknown"}, // even unknown paths do not reveal anything to anonymous clients
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			requireUnauthorized(t, e.do(t, tc.method, tc.path, ""))
		})
	}
}

func TestAuthUnauthorizedUploadHasNoSideEffects(t *testing.T) {
	e := setup(t, withToken(testToken))

	body, ct := uploadBody(t, "file", "a.xlsx", []byte("data"))
	resp, err := http.Post(e.ts.URL+"/upload", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	requireUnauthorized(t, resp)

	if n := e.ms.count(); n != 0 {
		t.Errorf("%d messages were saved for an unauthorized upload", n)
	}
	if names := dirNames(t, e.in); len(names) != 0 {
		t.Errorf("files in the input folder: %v", names)
	}
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("files in the tmp folder: %v", names)
	}
	if runs := e.job.runs.Load(); runs != 0 {
		t.Errorf("the job was started %d times", runs)
	}
}

func TestAuthAuthorizedUploadWorks(t *testing.T) {
	e := setup(t, withToken(testToken))

	body, ct := uploadBody(t, "file", "a.xlsx", []byte("data"))
	req, err := http.NewRequest("POST", e.ts.URL+"/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if n := e.ms.count(); n != 1 {
		t.Errorf("%d messages saved", n)
	}
}

func TestAuthTokenSources(t *testing.T) {
	t.Run("settings", func(t *testing.T) {
		e := setup(t, withToken(testToken))
		requireReachedHandler(t, e.do(t, "GET", "/check", "Bearer "+testToken))
	})

	t.Run("environment overrides settings", func(t *testing.T) {
		t.Setenv("GOCSVIFY_AUTH_TOKEN", "from-env")
		e := setup(t, withToken(testToken))
		requireUnauthorized(t, e.do(t, "GET", "/check", "Bearer "+testToken))
		requireReachedHandler(t, e.do(t, "GET", "/check", "Bearer from-env"))
	})

	t.Run("environment alone", func(t *testing.T) {
		t.Setenv("GOCSVIFY_AUTH_TOKEN", "from-env")
		e := setup(t, nil)
		requireUnauthorized(t, e.do(t, "GET", "/check", ""))
		requireReachedHandler(t, e.do(t, "GET", "/check", "Bearer from-env"))
	})

	t.Run("empty environment falls back to settings", func(t *testing.T) {
		t.Setenv("GOCSVIFY_AUTH_TOKEN", "")
		e := setup(t, withToken(testToken))
		requireUnauthorized(t, e.do(t, "GET", "/check", ""))
		requireReachedHandler(t, e.do(t, "GET", "/check", "Bearer "+testToken))
	})
}

func TestResolveAuthToken(t *testing.T) {
	for _, tc := range []struct {
		name, env, setting, want string
	}{
		{"nothing", "", "", ""},
		{"settings only", "", "abc", "abc"},
		{"env only", "abc", "", "abc"},
		{"env wins", "env", "setting", "env"},
		{"env trimmed", " abc\n", "", "abc"},
		{"settings trimmed", "", "  abc ", "abc"},
		{"whitespace env falls back", " \n", "setting", "setting"},
		{"whitespace only", " ", "\t", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOCSVIFY_AUTH_TOKEN", tc.env)
			got := resolveAuthToken(convertSettings.Settings{AuthToken: tc.setting})
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// AuthMiddleware on its own: the wrapped handler is not called for rejected requests.
func TestAuthMiddlewareDoesNotCallHandlerWhenRejected(t *testing.T) {
	t.Parallel()

	calls := 0
	h := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}), testToken)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Errorf("code %d, calls %d", rec.Code, calls)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || calls != 1 {
		t.Errorf("code %d, calls %d", rec.Code, calls)
	}
}

func TestAuthMiddlewareEmptyTokenIsPassThrough(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	AuthMiddleware(inner, "").ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("code %d", rec.Code)
	}
}
