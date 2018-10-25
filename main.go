package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/saracen/lfscache/server"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		httpAddr     = flag.String("http-addr", ":8080", "HTTP listen address")
		lfsServerURL = flag.String("url", "", "LFS server URL")
		directory    = flag.String("directory", "./objects", "cache directory")
		printVersion = flag.Bool("v", false, "print version")
	)

	flag.Parse()

	if *printVersion {
		fmt.Printf("%v, commit %v, built at %v\n", version, commit, date)
		os.Exit(0)
	}

	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
		logger = level.NewFilter(logger, level.AllowInfo())
		logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	}

	addr, err := url.Parse(*lfsServerURL)
	if err == nil && (addr.Scheme != "http" && addr.Scheme != "https") {
		err = errors.New("unsupported LFS server URL")
	}
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}

	s, err := server.New(logger, addr.String(), *directory)
	if err != nil {
		panic(err)
	}

	level.Info(logger).Log("event", "listening", "proxy-endpoint", addr.String(), "transport", "HTTP", "addr", *httpAddr)
	panic(http.ListenAndServe(*httpAddr, s.Handle()))
}
