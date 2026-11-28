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

func prepareProviderChat(addr, provider, models, base, volce, dir string, headersJSON *string) (*server.Server, func(), error) {
	generic := provider != "" || models != "" || base != ""
	if dir == "" || (!generic && volce == "") || len(models) > 16<<10 || len(volce) > 16<<10 {
		return nil, nil, errors.New("provider Chat requires a journal and bounded model lists")
	}
	if generic && (provider == "" || models == "" || base == "") {
		return nil, nil, errors.New("generic Chat requires provider, models and endpoint")
	}
	if !generic && headersJSON != nil {
		return nil, nil, errors.New("outbound headers require a generic Chat connection")
	}
	options := singlepilot.ChatOptions{HeadersJSON: headersJSON}
	if proxy, configured := os.LookupEnv("GO_ROUTER_GENERIC_CHAT_STRICT_PROXY_URL"); configured {
		if volce != "" {
			return nil, nil, errors.New("the generic strict proxy cannot be combined with a native VolceAPI connection")
		}
		options.StrictProxy = &proxy
	}
	journal, err := attemptlog.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	closeJournal := func() { _ = journal.Close() }
	names, key := strings.Split(models, ","), os.Getenv("GO_ROUTER_GENERIC_CHAT_KEY")
	var routes http.Handler
	if volce == "" {
		routes, err = singlepilot.NewConfigured(provider, names, base, key, journal, options)
	} else {
		connections := []singlepilot.Connection{singlepilot.Volce(strings.Split(volce, ","), os.Getenv("GO_ROUTER_VOLCE_CHAT_KEY"))}
		if generic {
			c, configErr := singlepilot.GenericConfigured(provider, names, base, key, headersJSON)
			if configErr != nil {
				closeJournal()
				return nil, nil, configErr
			}
			connections = append(connections, c)
		}
		routes, err = singlepilot.NewConnections(journal, connections...)
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
