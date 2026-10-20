package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"poggers.institute/freshbreath/internal/formats"
)

func TestTaskArgValue(t *testing.T) {
	cases := []struct {
		name string
		v    interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"string with spaces", "hello world", "hello world"},
		{"float64 whole", float64(42), "42"},
		{"float64 decimal", float64(3.14), "3.14"},
		{"int", int(7), "7"},
		{"int64", int64(99), "99"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"array", []interface{}{"a", "b"}, `["a","b"]`},
		{"map", map[string]interface{}{"k": "v"}, `{"k":"v"}`},
		{"nil", nil, "null"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := taskArgValue(tc.v)
			if got != tc.want {
				t.Errorf("taskArgValue(%#v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

// A task script runs under bash with nothing of the server's environment
// but PATH, in a temp HOME (also its working dir) that's gone afterwards.
// Fresh Breath's directories and the calling app come in as FRBR_ vars.
func TestRunTaskEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash environment test")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	t.Setenv("FRBR_SIGNING_KEY", "secret")
	task := &formats.Task{Name: "env", Script: `cat "$TASK_UPLOAD" >/dev/null
printf '{"bash":"%s","home":"%s","pwd":"%s","upload":"%s","path":"%s","dir":"%s","app_dir":"%s","app_nonce":"%s","env":"%s"}' \
  "$BASH_VERSION" "$HOME" "$PWD" "$TASK_UPLOAD" "$PATH" "$FRBR_DIR" "$FRBR_APP_DIR" "$FRBR_APP_NONCE" \
  "$(env | cut -d= -f1 | sort | tr '\n' ' ')"`}
	files := map[string]taskFile{"upload": {name: "a.txt", data: []byte("hi")}}
	srv := &Server{config: Config{Dir: "/opt/frbr", DataDir: "/var/frbr"}}

	result, err := srv.runTask(t.Context(), task, "abc123", map[string]interface{}{"note": "x"}, files, "tok")
	if err != nil || result.IsError {
		t.Fatalf("runTask: %v %q", err, taskResultText(result))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(taskResultText(result)), &got); err != nil {
		t.Fatalf("output %q: %v", taskResultText(result), err)
	}
	if got["bash"] == "" {
		t.Error("script didn't run under bash")
	}
	if got["home"] == "" || got["pwd"] != got["home"] || got["upload"] != got["home"]+"/a.txt" {
		t.Errorf("home/pwd/upload = %q/%q/%q, want one temp dir", got["home"], got["pwd"], got["upload"])
	}
	if _, err := os.Stat(got["home"]); !os.IsNotExist(err) {
		t.Errorf("task home %s still exists", got["home"])
	}
	if got["path"] != os.Getenv("PATH") {
		t.Errorf("PATH = %q, want the server's", got["path"])
	}
	if strings.Contains(got["env"], "FRBR_SIGNING_KEY") {
		t.Errorf("server environment leaked: %s", got["env"])
	}
	if got["dir"] != "/opt/frbr" || got["app_dir"] != "/var/frbr/apps/abc123" || got["app_nonce"] != "abc123" {
		t.Errorf("FRBR_DIR/APP_DIR/APP_NONCE = %q/%q/%q", got["dir"], got["app_dir"], got["app_nonce"])
	}
	for _, k := range []string{"TASK", "TASK_NOTE", "TASK_UPLOAD", "TASK_TOKEN"} {
		if !strings.Contains(" "+got["env"], " "+k+" ") {
			t.Errorf("env %q is missing %s", got["env"], k)
		}
	}
}

// With no calling app (the /mcp/ path) the app variables are left unset.
func TestRunTaskWithoutApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash environment test")
	}
	task := &formats.Task{Name: "env", Script: `env | cut -d= -f1 | grep '^FRBR_' | sort | tr '\n' ' '`}
	srv := &Server{config: Config{Dir: "/opt/frbr", DataDir: "/var/frbr"}}
	result, err := srv.runTask(t.Context(), task, "", nil, nil, "")
	if err != nil || result.IsError {
		t.Fatalf("runTask: %v %q", err, taskResultText(result))
	}
	if got := taskResultText(result); got != "FRBR_DIR " {
		t.Errorf("FRBR_ vars = %q, want only FRBR_DIR", got)
	}
}
