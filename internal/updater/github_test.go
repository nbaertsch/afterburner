package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Error("authorization header missing")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"checksums.txt","size":10,"url":"asset"}]}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), Token: "secret", APIBase: server.URL}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.2.3" {
		t.Fatalf("tag = %s", release.TagName)
	}
}

func TestVersionComparison(t *testing.T) {
	if !IsNewer("v1.2.3", "v1.3.0") || IsNewer("v1.3.0", "v1.2.9") {
		t.Fatal("version comparison is incorrect")
	}
}
