package main

import (
	"errors"
	"os"
	"strings"

	"github.com/codeasier/go-router/internal/attemptlog"
	"github.com/codeasier/go-router/internal/server"
	"github.com/codeasier/go-router/internal/singlepilot"
)

func prepareProviderChat(addr, provider, models, base, volce, xai, xiaomi, region, dir string) (*server.Server, func(), error) {
	if dir == "" || len(models) > 16<<10 || len(volce) > 16<<10 || len(xai) > 16<<10 || len(xiaomi) > 16<<10 {
		return nil, nil, errors.New("provider Chat requires a journal and bounded model lists")
	}
	var connections []singlepilot.Connection
	if provider != "" || models != "" || base != "" {
		if provider == "" || models == "" || base == "" {
			return nil, nil, errors.New("generic Chat requires provider, models and endpoint")
		}
		connection, err := singlepilot.Generic(provider, strings.Split(models, ","), base, os.Getenv("GO_ROUTER_GENERIC_CHAT_KEY"))
		if err != nil {
			return nil, nil, err
		}
		connections = append(connections, connection)
	}
	if volce != "" {
		connections = append(connections, singlepilot.Volce(strings.Split(volce, ","), os.Getenv("GO_ROUTER_VOLCE_CHAT_KEY")))
	}
	if xai != "" {
		connections = append(connections, singlepilot.XAI(strings.Split(xai, ","), os.Getenv("GO_ROUTER_XAI_CHAT_KEY")))
	}
	if xiaomi != "" {
		connection, err := singlepilot.Xiaomi(strings.Split(xiaomi, ","), region, os.Getenv("GO_ROUTER_XIAOMI_CHAT_KEY"))
		if err != nil {
			return nil, nil, err
		}
		connections = append(connections, connection)
	} else if region != "" {
		return nil, nil, errors.New("Xiaomi region requires Xiaomi Chat models")
	}
	journal, err := attemptlog.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	closeJournal := func() { _ = journal.Close() }
	routes, err := singlepilot.New(journal, connections...)
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
