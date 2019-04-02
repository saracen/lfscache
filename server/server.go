package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/git-lfs/git-lfs/tools/humanize"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/saracen/lfscache/cache"
)

// BatchResponse represents a batch response payload.
//
// https://github.com/git-lfs/git-lfs/blob/master/docs/api/batch.md#successful-responses
type BatchResponse struct {
	Transfer string                 `json:"transfer,omitempty"`
	Objects  []*BatchObjectResponse `json:"objects"`
}

// BatchObjectResponse is the object item of a BatchResponse
type BatchObjectResponse struct {
	OID           string                                `json:"oid"`
	Size          int64                                 `json:"size"`
	Authenticated bool                                  `json:"authenticated,omitempty"`
	Actions       map[string]*BatchObjectActionResponse `json:"actions"`
}

// BatchObjectActionResponse is the action item of a BatchObjectResponse
type BatchObjectActionResponse struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

const (
	// UpstreamHeaderList is a list of headers to be used when fetching the
	// original content location.
	UpstreamHeaderList = "X-Lfs-Cache-Header-List"

	// OriginalHrefHeader is the href link to the original content location.
	OriginalHrefHeader = "X-Lfs-Cache-Original-Href"

	// SizeHeader is the size of the content to be downloaded.
	SizeHeader = "X-Lfs-Cache-Size"

	// SignatureHeader is a signature used to prove the server is the author of
	// additional headers.
	SignatureHeader = "X-Lfs-Signature"

	// ContentCachePathPrefix is the path prefix for cached content delivery.
	ContentCachePathPrefix = "/_lfs_cache/"
)

type contextKey string

var contextKeyOriginalHost = contextKey("original-host")

type originalHost struct {
	http bool
	host string
}

// Server is a LFS caching server.
type Server struct {
	logger   log.Logger
	upstream *url.URL
	mux      *http.ServeMux
	cache    *cache.FilesystemCache
	client   *http.Client
	hmacKey  [64]byte
}

// New returns a new LFS proxy caching server.
func New(logger log.Logger, upstream, directory string) (*Server, error) {
	return newServer(logger, upstream, directory, true)
}

// NewNoCache returns a new LFS proxy server, with no caching.
func NewNoCache(logger log.Logger, upstream string) (*Server, error) {
	return newServer(logger, upstream, "", false)
}

func newServer(logger log.Logger, upstream, directory string, cacheEnabled bool) (*Server, error) {
	var fs *cache.FilesystemCache
	var err error
	if cacheEnabled {
		fs, err = cache.NewFilesystemCache(directory)
		if err != nil {
			return nil, err
		}
	}

	s := &Server{
		logger: logger,
		cache:  fs,
		client: &http.Client{
			Transport: &http.Transport{
				Dial: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).Dial,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}

	_, err = rand.Read(s.hmacKey[:])
	if err != nil {
		return nil, err
	}

	if s.upstream, err = url.Parse(upstream); err != nil {
		return nil, err
	}

	// ensure upstream path has suffixed separator
	if !strings.HasSuffix(s.upstream.Path, "/") {
		s.upstream.Path += "/"
	}

	s.mux = http.NewServeMux()
	if s.cache != nil {
		s.mux.HandleFunc(ContentCachePathPrefix, s.serve)
	} else {
		s.mux.Handle(ContentCachePathPrefix, s.nocache())
	}
	s.mux.Handle("/objects/batch", s.batch())
	s.mux.Handle("/", s.proxy())

	return s, nil
}

// Logger returns the server logger.
func (s *Server) Logger() log.Logger {
	return s.logger
}

// Handle returns this server's http.Handler.
func (s *Server) Handle() http.Handler {
	return s.mux
}

func (s *Server) proxy() *httputil.ReverseProxy {
	director := func(req *http.Request) {
		outreq := req.WithContext(context.WithValue(req.Context(), contextKeyOriginalHost, &originalHost{
			http: req.TLS == nil,
			host: req.Host,
		}))
		*req = *outreq

		req.URL.Path = strings.TrimLeft(req.URL.Path, "/")
		req.URL = s.upstream.ResolveReference(req.URL)
		req.Host = req.URL.Host

		if _, ok := req.Header["User-Agent"]; !ok {
			req.Header.Set("User-Agent", "")
		}
	}

	errorHandler := func(w http.ResponseWriter, r *http.Request, err error) {
		level.Error(s.logger).Log("event", "proxying", "request", r.URL, "err", err)
	}

	return &httputil.ReverseProxy{Director: director, ErrorHandler: errorHandler}
}

func (s *Server) batch() *httputil.ReverseProxy {
	proxy := s.proxy()
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.StatusCode != http.StatusOK {
			level.Error(s.logger).Log("event", "proxying", "request", r.Request.URL, "err", fmt.Sprintf("remote server responded with %d status code", r.StatusCode))
			return nil
		}

		var err error
		var compress bool
		if !r.Uncompressed && strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
			compress = true
			if r.Body, err = gzip.NewReader(r.Body); err != nil {
				return err
			}
		}

		var br BatchResponse
		if err = json.NewDecoder(r.Body).Decode(&br); err != nil {
			return err
		}

		// only support basic transfers
		if br.Transfer != "" && br.Transfer != "basic" {
			return s.batchResponse(&br, compress, r)
		}

		// modify batch request urls
		for _, object := range br.Objects {
			for operation, action := range object.Actions {
				if operation != "download" {
					continue
				}
				if action.Header == nil {
					action.Header = make(map[string]string)
				}

				host, ok := r.Request.Context().Value(contextKeyOriginalHost).(*originalHost)
				if !ok {
					panic("lfscache error: original host information not set")
				}

				list := make([]string, 0, len(action.Header))
				for header := range action.Header {
					list = append(list, header)
				}

				scheme := "http"
				if !host.http {
					scheme = "https"
				}

				action.Header[UpstreamHeaderList] = strings.Join(list, ";")
				action.Header[OriginalHrefHeader] = action.Href
				action.Header[SizeHeader] = strconv.Itoa(int(object.Size))
				action.Href = (&url.URL{
					Scheme: scheme,
					Host:   host.host,
					Path:   ContentCachePathPrefix + object.OID,
				}).String()

				mac := hmac.New(sha256.New, s.hmacKey[:])
				mac.Write([]byte(action.Header[UpstreamHeaderList]))
				mac.Write([]byte(action.Header[OriginalHrefHeader]))
				mac.Write([]byte(action.Header[SizeHeader]))

				action.Header[SignatureHeader] = hex.EncodeToString(mac.Sum(nil))
			}
		}

		return s.batchResponse(&br, compress, r)
	}

	return proxy
}

func (s *Server) batchResponse(br *BatchResponse, compress bool, r *http.Response) error {
	var err error
	if err = r.Body.Close(); err != nil {
		return err
	}

	buf := new(bytes.Buffer)

	// gzip compress if the original response did
	w := nopCloser(buf)
	if compress {
		w = gzip.NewWriter(buf)
	}

	if err = json.NewEncoder(w).Encode(br); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}

	r.Body = ioutil.NopCloser(buf)
	r.ContentLength = int64(buf.Len())
	r.Header.Set("Content-Length", strconv.Itoa(buf.Len()))

	return nil
}

func (s *Server) nocache() *httputil.ReverseProxy {
	director := func(req *http.Request) {
		addr, _, header, err := s.parseHeaders(req)
		if err != nil {
			return
		}

		originalURL, err := url.Parse(addr)
		if err != nil {
			return
		}

		req.URL = originalURL
		req.Header = header
	}

	errorHandler := func(w http.ResponseWriter, r *http.Request, err error) {
		level.Error(s.logger).Log("event", "proxying-no-cache", "request", r.URL, "err", err)
	}

	return &httputil.ReverseProxy{Director: director, ErrorHandler: errorHandler}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	url, size, header, err := s.parseHeaders(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	begin := time.Now()
	oid := path.Base(r.URL.Path)
	cr, cw, source, err := s.cache.Get(oid)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	level.Info(s.logger).Log("event", "serving", "oid", oid, "source", source)
	defer func() {
		logger := log.With(s.logger, "event", "served", "oid", oid, "source", source, "took", time.Since(begin))
		if err != nil {
			level.Error(logger).Log("err", err)
		} else {
			rate := humanize.FormatByteRate(uint64(size), time.Since(begin))

			level.Info(logger).Log("size", size, "rate", rate)
		}
	}()

	if cw != nil {
		go s.fetch(cw, oid, url, size, header)
	}

	defer cr.Close()
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(cr, 0, int64(size)))
}

func (s *Server) parseHeaders(r *http.Request) (url string, size int, header http.Header, err error) {
	// check header is valid
	signature, err := hex.DecodeString(r.Header.Get(SignatureHeader))
	if err != nil {
		return "", 0, nil, err
	}

	mac := hmac.New(sha256.New, s.hmacKey[:])
	mac.Write([]byte(r.Header.Get(UpstreamHeaderList)))
	mac.Write([]byte(r.Header.Get(OriginalHrefHeader)))
	mac.Write([]byte(r.Header.Get(SizeHeader)))

	if !hmac.Equal(mac.Sum(nil), signature) {
		return "", 0, nil, errors.New("invalid signature")
	}

	header = make(http.Header)
	for _, key := range strings.Split(r.Header.Get(UpstreamHeaderList), ";") {
		if key == "" {
			continue
		}
		header.Add(key, r.Header.Get(key))
	}

	if size, err = strconv.Atoi(r.Header.Get(SizeHeader)); err != nil {
		return "", 0, header, err
	}

	url = r.Header.Get(OriginalHrefHeader)

	return
}

func (s *Server) fetch(w io.Writer, oid, url string, size int, header http.Header) (err error) {
	level.Info(s.logger).Log("event", "fetching", "oid", oid)

	hcw := &hashCountWriter{
		h: sha256.New(),
		w: w,
	}

	begin := time.Now()
	var beginTransfer time.Time
	defer func() {
		rate := humanize.FormatByteRate(uint64(hcw.n), time.Since(beginTransfer))

		logger := log.With(s.logger, "event", "fetched", "oid", oid, "took", time.Since(begin), "downloaded", fmt.Sprintf("%d/%d", hcw.n, size), "rate", rate)
		if err != nil {
			level.Error(logger).Log("err", err)
		} else {
			level.Info(logger).Log()
		}

		err := s.cache.Done(oid, err)
		if err != nil {
			panic(err)
		}
	}()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header = header
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream server responded with %d status", resp.StatusCode)
	}

	defer resp.Body.Close()

	beginTransfer = time.Now()
	_, err = io.Copy(hcw, resp.Body)
	if err == nil {
		if oid != hex.EncodeToString(hcw.h.Sum(nil)) {
			return fmt.Errorf("file checksum mismatch")
		}
	}

	return err
}

type nc struct {
	io.Writer
}

func (nc) Close() error {
	return nil
}

func nopCloser(w io.Writer) io.WriteCloser {
	return nc{w}
}

type hashCountWriter struct {
	n int
	h hash.Hash
	w io.Writer
}

func (hcw *hashCountWriter) Write(p []byte) (n int, err error) {
	n, err = hcw.w.Write(p)
	hcw.n += n
	hcw.h.Write(p[:n])
	return
}
