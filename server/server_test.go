package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func server() (*httptest.Server, *Server, string, error) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/objects/batch":
			response := BatchResponse{
				Transfer: "basic",
				Objects: []*BatchObjectResponse{
					&BatchObjectResponse{
						OID:           "1111111",
						Size:          123,
						Authenticated: true,
						Actions: map[string]*BatchObjectActionResponse{
							"download": &BatchObjectActionResponse{
								Href: ts.URL + "/download",
							},
						},
					},
				},
			}

			json.NewEncoder(w).Encode(response)

		default:
			fmt.Fprintf(w, "upstream")
		}
	}))

	dir, err := ioutil.TempDir("", "")
	if err != nil {
		return ts, nil, dir, err
	}

	s, err := New(log.NewNopLogger(), ts.URL, dir)

	return ts, s, dir, err
}

func TestProxy(t *testing.T) {
	ts, s, dir, err := server()
	defer os.RemoveAll(dir)
	defer ts.Close()

	require.NoError(t, err)
	w := httptest.NewRecorder()

	req := httptest.NewRequest("POST", ts.URL+"/anything", nil)
	s.Handle().ServeHTTP(w, req)

	body, _ := ioutil.ReadAll(w.Body)
	assert.Equal(t, body, []byte("upstream"))
}

func TestBatch(t *testing.T) {
	ts, s, dir, err := server()
	defer os.RemoveAll(dir)
	defer ts.Close()

	require.NoError(t, err)
	w := httptest.NewRecorder()

	var br BatchResponse
	{
		req := httptest.NewRequest("POST", ts.URL+"/objects/batch", nil)
		s.Handle().ServeHTTP(w, req)

		require.NoError(t, json.NewDecoder(w.Body).Decode(&br))
	}

	require.Len(t, br.Objects, 1)
	require.Contains(t, br.Objects[0].Actions, "download")
	action := br.Objects[0].Actions["download"]

	req := httptest.NewRequest("POST", action.Href, nil)
	for key, val := range action.Header {
		req.Header.Add(key, val)
	}
	s.Handle().ServeHTTP(w, req)

	body, _ := ioutil.ReadAll(w.Body)
	assert.Equal(t, body, []byte("upstream"))
}
