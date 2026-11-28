package main

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/server"
	"github.com/codeasier/go-router/internal/singlepilot"
)

func prepareGenericChat(addr, provider, models, base, dir string) (*server.Server, func(), error) {
	if provider == "" || models == "" || base == "" || dir == "" || len(models) > 16<<10 {
		return nil, nil, errors.New("generic Chat requires provider, models, endpoint and journal")
	}
	journal, err := attemptlog.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	closeJournal := func() { _ = journal.Close() }
	names, key := strings.Split(models, ","), os.Getenv("GO_ROUTER_GENERIC_CHAT_KEY")
	var routes http.Handler
	if proxy, configured := os.LookupEnv("GO_ROUTER_GENERIC_CHAT_STRICT_PROXY_URL"); configured {
		routes, err = singlepilot.NewWithStrictProxy(provider, names, base, key, journal, proxy)
	} else {
		routes, err = singlepilot.New(provider, names, base, key, journal)
	}
	if err != nil {
		closeJournal()
		return nil, nil, err
	}
	srv, err := server.NewLocalProvider(addr, routes)
	if err != nil {
		closeJournal()
		return nil, nil, err
	}
	return srv, closeJournal, nil
}
