package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	pbtests "github.com/pocketbase/pocketbase/tests"
)

// collectionServer serves a followers collection of seven actors, paged by
// size in the style of one server implementation.
func collectionServer(t *testing.T, style string, size int) string {
	t.Helper()

	actors := make([]string, 7)
	for i := range actors {
		actors[i] = fmt.Sprintf("https://remote.example/users/u%d", i)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/alice/followers" {
			http.NotFound(w, r)
			return
		}
		coll := srv.URL + r.URL.Path
		q := r.URL.Query()

		// start is the index of the page's first item, or -1 for the root.
		start := -1
		switch style {
		case "mastodon", "wanderer", "wanderer-old":
			if n, err := strconv.Atoi(q.Get("page")); err == nil {
				start = (n - 1) * size
			} else if style == "wanderer-old" {
				start = 0
			}
		case "gotosocial":
			if q.Has("max_id") {
				start, _ = strconv.Atoi(q.Get("max_id"))
			} else if q.Has("limit") {
				start = 0
			}
		}

		var body map[string]any
		if start < 0 {
			first := coll + "?page=1"
			if style == "gotosocial" {
				first = coll + "?limit=" + strconv.Itoa(size)
			}
			body = map[string]any{"type": "OrderedCollection", "id": coll, "totalItems": len(actors), "first": first}
		} else {
			end := min(start+size, len(actors))
			body = map[string]any{"type": "OrderedCollectionPage", "totalItems": len(actors), "orderedItems": actors[start:end]}
			if end < len(actors) {
				switch style {
				case "gotosocial":
					body["next"] = fmt.Sprintf("%s?limit=%d&max_id=%d", coll, size, end)
				case "wanderer-old":
					// Older wanderer versions pointed followers' next at the outbox.
					body["next"] = fmt.Sprintf("%s/users/alice/outbox?page=%d", srv.URL, end/size+1)
				default:
					body["next"] = fmt.Sprintf("%s?page=%d", coll, end/size+1)
				}
			}
		}
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/users/alice/followers"
}

func TestFetchCollectionPage(t *testing.T) {
	t.Setenv("POCKETBASE_ENCRYPTION_KEY", summitLogTestKey)
	defaultClient := newHTTPClient
	newHTTPClient = func() *http.Client { return http.DefaultClient }
	t.Cleanup(func() { newHTTPClient = defaultClient })

	app, err := pbtests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	want := func(from, to int) []string {
		var out []string
		for i := from; i < to; i++ {
			out = append(out, fmt.Sprintf("https://remote.example/users/u%d", i))
		}
		return out
	}

	for _, style := range []string{"mastodon", "gotosocial", "wanderer", "wanderer-old"} {
		t.Run(style, func(t *testing.T) {
			url := collectionServer(t, style, 3)
			for page, items := range map[int][]string{1: want(0, 3), 2: want(3, 6), 3: want(6, 7), 4: nil} {
				got, err := FetchCollectionPage(app, context.Background(), url, page)
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				var ids []string
				for _, it := range got.OrderedItems {
					ids = append(ids, it.GetLink().String())
				}
				if !reflect.DeepEqual(ids, items) {
					t.Errorf("page %d = %v, want %v", page, ids, items)
				}
				if got.TotalItems != 7 {
					t.Errorf("page %d: totalItems = %d, want 7", page, got.TotalItems)
				}
			}
		})
	}
}

func TestFetchCollectionPageStaysOnHost(t *testing.T) {
	t.Setenv("POCKETBASE_ENCRYPTION_KEY", summitLogTestKey)
	defaultClient := newHTTPClient
	newHTTPClient = func() *http.Client { return http.DefaultClient }
	t.Cleanup(func() { newHTTPClient = defaultClient })

	app, err := pbtests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "OrderedCollectionPage", "orderedItems": []string{"https://remote.example/users/u0"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "OrderedCollection", "totalItems": 1, "first": "https://elsewhere.example/followers?page=1",
		})
	}))
	t.Cleanup(srv.Close)

	got, err := FetchCollectionPage(app, context.Background(), srv.URL+"/followers", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OrderedItems) != 1 {
		t.Fatalf("items = %v, want the page from the collection's own host", got.OrderedItems)
	}
}
