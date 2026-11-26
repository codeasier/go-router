package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/codeasier/go-router/internal/controlplane"
	"github.com/codeasier/go-router/internal/nodeconfig"
)

const preflightUsage = "Usage: go-router-dashboard preflight -config <private inventory> -candidate <node JSON file> -node <id> [-node <id> ...] [-check]"

func runPreflight(ctx context.Context, args []string, lookup func(string) (string, bool), out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "private JSON inventory file")
	candidatePath := flags.String("candidate", "", "trusted immutable local node JSON file")
	check := flags.Bool("check", false, "check selected loopback nodes with read-only status GETs")
	var ids []string
	flags.Func("node", "registered node ID, repeat for each target", func(id string) error {
		ids = append(ids, id)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(diagnostic, preflightUsage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" || *candidatePath == "" || len(ids) == 0 || flags.NArg() != 0 {
		fmt.Fprintln(diagnostic, preflightUsage)
		return 2
	}
	snapshot, err := nodeconfig.Load(*candidatePath)
	if err != nil {
		fmt.Fprintln(diagnostic, err) // The loader returns only fixed, redacted errors.
		return 2
	}
	if !*check {
		lookup = nil
	}
	nodes, err := loadConfig(*configPath, lookup)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 2
	}
	sum := snapshot.Digest()
	target := controlplane.ConfigIdentity{Revision: snapshot.Revision(), Digest: hex.EncodeToString(sum[:])}
	var report *controlplane.ConfigPreflight
	if *check {
		var collector *controlplane.Collector
		collector, err = controlplane.NewCollector(nodes, time.Second, time.Second)
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 2
		}
		report, err = collector.CheckConfiguration(ctx, target, ids)
	} else {
		report, err = controlplane.PreviewConfiguration(ctx, nodes, target, ids)
	}
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(diagnostic, "Preflight canceled.")
			return 1
		}
		fmt.Fprintln(diagnostic, err)
		return 2
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(diagnostic, "Unable to write preflight report.")
		return 1
	}
	if report.Converged != nil && !*report.Converged {
		return 1
	}
	return 0
}
