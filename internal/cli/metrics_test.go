package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// nan metrics usage printed the platform's answer as it came, every field
// included. It prints the usage this CLI reads: the periods and the models
// in them, nothing the platform keeps for itself.
func TestMetricsUsagePrintsOnlyTheUsage(t *testing.T) {
	fakeHome(t, `{"token":"t"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"last24h":{"totalTokens":3,"byModel":[{"model":"glm","inputTokens":1,"outputTokens":2}]},` +
			`"last30d":{"totalTokens":3},"allTime":{"totalTokens":3},` +
			`"internalId":"id-1","account":{"owner":"someone@example.com"}}`))
	}))
	t.Cleanup(srv.Close)
	swap(t, &apiBaseURL, srv.URL)

	out := captureStdout(t, func() {
		if code, msg := execute(t, "metrics", "usage"); code != 0 {
			t.Fatalf("exit %d: %s", code, msg)
		}
	})
	for _, want := range []string{`"last24h"`, `"glm"`, `"outputTokens": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("the usage lacks %s:\n%s", want, out)
		}
	}
	for _, gone := range []string{"internalId", "someone@example.com", "account"} {
		if strings.Contains(out, gone) {
			t.Errorf("printed %q, which is not usage:\n%s", gone, out)
		}
	}
}

// captureStdout is what f printed with fmt.Print*, which writes to
// os.Stdout and not to the command's writer.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	os.Stdout = old
	_ = w.Close()
	return <-done
}
