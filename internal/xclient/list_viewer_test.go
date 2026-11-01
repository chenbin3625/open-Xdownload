package xclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListMetadataViewerIsReusedForMembers(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			clients := make([]*Client, 0, 2)
			for _, name := range []string{"primary", "backup"} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/ListByRestId") {
						if fallback && name == "primary" {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						fmt.Fprintf(w, `{"data":{"list":{"id_str":"l1","name":%q,"member_count":2}}}`, name)
						return
					}
					if strings.Contains(r.URL.Query().Get("variables"), "next-page") {
						fmt.Fprint(w, listMembersPage([]string{name + "-2"}, ""))
					} else {
						fmt.Fprint(w, listMembersPage([]string{name + "-1"}, "next-page"))
					}
				}))
				t.Cleanup(server.Close)
				clients = append(clients, &Client{http: server.Client(), baseURL: server.URL, limiter: newRateLimiter(), retryBackoff: func(int) time.Duration { return 0 }})
			}
			pool := &Pool{clients: clients}
			list, err := pool.GetListByID(context.Background(), "l1")
			if err != nil {
				t.Fatal(err)
			}
			users, err := pool.GetListMembers(context.Background(), list)
			if err != nil {
				t.Fatal(err)
			}
			if len(users) != 2 || users[0].ID != list.Name+"-1" || users[1].ID != list.Name+"-2" {
				t.Fatalf("metadata viewer %s changed for members: %+v", list.Name, users)
			}
		})
	}
}
