package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installerResponse(status, version string) *httptest.ResponseRecorder {
	a := &API{cliReleases: &cliReleaseCache{
		value: cliReleaseInfo{Status: status, Version: version}, expires: time.Now().Add(time.Minute),
	}}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/install.sh", nil))
	return w
}

func TestCLIInstallPublicAndUnavailable(t *testing.T) {
	w := installerResponse("available", "v0.1.0-rc.1")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public installer response: %d %v", w.Code, w.Header())
	}
	for _, tc := range []struct{ status, version string }{
		{"unpublished", ""}, {"unavailable", ""}, {"available", "v0.1.0; touch unsafe"},
	} {
		w := installerResponse(tc.status, tc.version)
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "herdrx_install()") {
			t.Fatalf("invalid release offered an installer: %d %s", w.Code, w.Body)
		}
	}
}

func TestCLIInstallShell(t *testing.T) {
	// Run the actual response with a fake downloader and release installer.
	// This checks version pinning, failure propagation and temporary-file cleanup
	// without using the network or installing anything in the user's home.
	for _, tc := range []struct {
		name       string
		curlExit   string
		scriptExit string
		wantExit   int
		wantRun    bool
	}{
		{"install prerelease", "0", "0", 0, true},
		{"partial download", "22", "0", 22, false},
		{"installer failure", "0", "17", 17, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			curl := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    https://*) printf '%s\n' "$1" > "$HERDRX_TEST_DIR/url"; shift ;;
    -o) output=$2; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$output" > "$HERDRX_TEST_DIR/temp-path"
cat > "$output" <<'SCRIPT'
printf '%s\n' "$@" > "$HERDRX_TEST_DIR/arguments"
exit "$HERDRX_TEST_SCRIPT_EXIT"
SCRIPT
exit "$HERDRX_TEST_CURL_EXIT"
`
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s", "--", "--install-dir", filepath.Join(dir, "custom bin"))
			cmd.Stdin = strings.NewReader(installerResponse("available", "v0.1.0-rc.1").Body.String())
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TMPDIR="+dir,
				"HERDRX_TEST_DIR="+dir, "HERDRX_TEST_CURL_EXIT="+tc.curlExit, "HERDRX_TEST_SCRIPT_EXIT="+tc.scriptExit)
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("shell exit: %v, output: %s", err, output)
			}
			url, err := os.ReadFile(filepath.Join(dir, "url"))
			if err != nil || strings.TrimSpace(string(url)) != cliRepositoryURL+"/releases/download/v0.1.0-rc.1/install-herdrx.sh" {
				t.Fatalf("release download: %s, %v", url, err)
			}
			arguments, err := os.ReadFile(filepath.Join(dir, "arguments"))
			if tc.wantRun {
				if err != nil || string(arguments) != "--version\nv0.1.0-rc.1\n--install-dir\n"+filepath.Join(dir, "custom bin")+"\n" {
					t.Fatalf("installer arguments: %s, %v", arguments, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("executed a partially downloaded installer")
			}
			temp, err := os.ReadFile(filepath.Join(dir, "temp-path"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(strings.TrimSpace(string(temp))); !os.IsNotExist(err) {
				t.Fatal("temporary installer was not cleaned up")
			}
		})
	}
}
