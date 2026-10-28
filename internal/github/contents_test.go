package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentsAndCreateRepo(t *testing.T) {
	var seen []string
	var putBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /user/repos":
			var in map[string]any
			_ = json.Unmarshal(b, &in)
			if in["private"] != true || in["auto_init"] != true || in["name"] != "gh-runners-machines" {
				t.Errorf("create body: %s", b)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"full_name":"octo/gh-runners-machines","private":true}`)
		case "GET /repos/octo/r/contents/machines":
			_, _ = io.WriteString(w, `[{"name":"a.json","path":"machines/a.json","sha":"s1","type":"file"}]`)
		case "GET /repos/octo/r/contents/machines/a.json":
			_, _ = io.WriteString(w, `{"name":"a.json","sha":"s1","content":"aGVs\nbG8="}`)
		case "PUT /repos/octo/r/contents/machines/a.json":
			_ = json.Unmarshal(b, &putBody)
			_, _ = io.WriteString(w, `{"content":{"sha":"s2"}}`)
		case "DELETE /repos/octo/r/contents/machines/a.json":
			_ = json.Unmarshal(b, &putBody)
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		}
	}))
	defer srv.Close()
	c := &Client{Token: "t", BaseURL: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()
	if r, err := c.CreateRepo(ctx, "gh-runners-machines", "x"); err != nil || !r.Private {
		t.Fatalf("CreateRepo: %v %+v", err, r)
	}
	l, err := c.ListContents(ctx, "octo", "r", "machines")
	if err != nil || len(l) != 1 || l[0].SHA != "s1" {
		t.Fatalf("ListContents: %v %+v", err, l)
	}
	f, err := c.GetContents(ctx, "octo", "r", "machines/a.json")
	if d, derr := f.Data(); err != nil || derr != nil || string(d) != "hello" {
		t.Fatalf("GetContents: %v %v %q", err, derr, d)
	}
	sha, err := c.PutContents(ctx, "octo", "r", "machines/a.json", "m", []byte("x"), "s1")
	if err != nil || sha != "s2" || putBody["sha"] != "s1" {
		t.Fatalf("PutContents: %v %q %v", err, sha, putBody)
	}
	if err := c.DeleteContents(ctx, "octo", "r", "machines/a.json", "m", "s2"); err != nil || putBody["sha"] != "s2" {
		t.Fatalf("DeleteContents: %v %v", err, putBody)
	}
	if _, err := c.GetContents(ctx, "octo", "r", "machines/none.json"); !IsNotFound(err) {
		t.Fatalf("missing file: %v", err)
	}
}
