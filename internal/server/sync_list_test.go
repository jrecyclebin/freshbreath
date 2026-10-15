package server

import (
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/pkg/sftp"
)

// An SFTP client talking to pkg/sftp's in-memory server over a pipe.
func memSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	a, b := net.Pipe()
	srv := sftp.NewRequestServer(b, sftp.InMemHandler())
	go srv.Serve()
	client, err := sftp.NewClientPipe(a, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); srv.Close() })
	return client
}

func memWrite(t *testing.T, c *sftp.Client, p string) {
	t.Helper()
	f, err := c.Create(p)
	if err != nil {
		t.Fatalf("create %s: %v", p, err)
	}
	io.WriteString(f, "x")
	f.Close()
}

func listPaths(t *testing.T, c *sftp.Client, dir string, recursive bool) []string {
	t.Helper()
	files, err := listRemote(c, dir, recursive)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f["path"].(string))
	}
	return paths
}

func TestListRemote(t *testing.T) {
	c := memSFTP(t)
	for _, d := range []string{"/shop", "/shop/p", "/shop/p/daisy", "/shop/.well-known", "/other"} {
		if err := c.Mkdir(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"/shop/index.html", "/shop/.htaccess", "/shop/p/daisy/index.html", "/shop/.well-known/x", "/other/secret"} {
		memWrite(t, c, f)
	}

	t.Run("one level", func(t *testing.T) {
		got := listPaths(t, c, "/shop", false)
		want := map[string]bool{"/shop/index.html": true, "/shop/p": true}
		if len(got) != len(want) || !want[got[0]] || !want[got[1]] {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("recursive: everything below, sorted, no hidden files or what's inside hidden dirs", func(t *testing.T) {
		got := listPaths(t, c, "/shop", true)
		want := []string{"/shop/index.html", "/shop/p", "/shop/p/daisy", "/shop/p/daisy/index.html"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("recursive: an empty dir is an empty list, a missing one an error", func(t *testing.T) {
		c.Mkdir("/empty")
		if got := listPaths(t, c, "/empty", true); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
		if _, err := listRemote(c, "/nope", true); err == nil {
			t.Fatal("want an error for a missing dir")
		}
	})
}
