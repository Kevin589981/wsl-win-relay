package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/autoforward"
	"github.com/Kevin589981/wsl-win-relay/internal/buildinfo"
	appconfig "github.com/Kevin589981/wsl-win-relay/internal/config"
)

type options struct {
	file           string
	config         string
	maxAge         time.Duration
	json           bool
	resolveNetwork string
	resolveWSL     string
}

type processChecker func(int) (bool, error)

func main() {
	if buildinfo.PrintRequested(os.Stdout, "wsl-win-relay-status", os.Args[1:]) {
		return
	}
	if code := run(os.Args[1:], os.Stdout, os.Stderr, time.Now, publisherAlive); code != 0 {
		os.Exit(code)
	}
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time, alive processChecker) int {
	opts, err := parseOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "wsl-win-relay-status: %v\n", err)
		return 2
	}
	path, err := statusPath(opts)
	if err != nil {
		fmt.Fprintf(stderr, "wsl-win-relay-status: %v\n", err)
		return 1
	}
	document, err := autoforward.ReadStatus(path)
	if err == nil {
		err = document.ValidateFresh(now(), opts.maxAge)
	}
	if err == nil {
		var running bool
		running, err = alive(document.ProcessID)
		if err == nil && !running {
			err = fmt.Errorf("status publisher process %d is not running", document.ProcessID)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "wsl-win-relay-status: %v\n", err)
		return 1
	}
	if opts.resolveNetwork != "" {
		if err := resolveMapping(stdout, document, opts.resolveNetwork, opts.resolveWSL); err != nil {
			fmt.Fprintf(stderr, "wsl-win-relay-status: %v\n", err)
			return 1
		}
		return 0
	}
	if opts.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(document); err != nil {
			fmt.Fprintf(stderr, "wsl-win-relay-status: write JSON: %v\n", err)
			return 1
		}
		return 0
	}
	if err := printStatus(stdout, document); err != nil {
		fmt.Fprintf(stderr, "wsl-win-relay-status: write status: %v\n", err)
		return 1
	}
	return 0
}

func parseOptions(args []string) (options, error) {
	opts := options{maxAge: autoforward.DefaultStatusMaxAge}
	set := flag.NewFlagSet("wsl-win-relay-status", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&opts.file, "file", "", "automatic mapping status JSON file")
	set.StringVar(&opts.config, "config", "", "relay configuration used to locate the status file")
	set.DurationVar(&opts.maxAge, "max-age", opts.maxAge, "maximum accepted status heartbeat age")
	set.BoolVar(&opts.json, "json", false, "emit the validated status document as JSON")
	set.StringVar(&opts.resolveNetwork, "resolve-network", "", "network family of one mapping to resolve")
	set.StringVar(&opts.resolveWSL, "resolve-wsl", "", "WSL address of one mapping to resolve")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", set.Args())
	}
	if opts.maxAge <= 0 {
		return options{}, errors.New("max-age must be positive")
	}
	if (opts.resolveNetwork == "") != (opts.resolveWSL == "") {
		return options{}, errors.New("resolve-network and resolve-wsl must be used together")
	}
	if opts.json && opts.resolveNetwork != "" {
		return options{}, errors.New("json output and mapping resolution cannot be used together")
	}
	return opts, nil
}

func statusPath(opts options) (string, error) {
	if opts.file != "" {
		return opts.file, nil
	}
	configPath := opts.config
	if configPath == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user configuration: %w", err)
		}
		configPath = filepath.Join(root, "wsl-win-relay", "config.json")
	}
	config, err := appconfig.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("load %s: %w", configPath, err)
	}
	if config.AutoForward.StatusFile == "" {
		return "", fmt.Errorf("automatic mapping status_file is not configured in %s", configPath)
	}
	return config.AutoForward.StatusFile, nil
}

func resolveMapping(writer io.Writer, document autoforward.StatusDocument, network, wslAddress string) error {
	var match *autoforward.MappingStatus
	for index := range document.Mappings {
		mapping := &document.Mappings[index]
		if mapping.Network != network || mapping.WSLAddress != wslAddress {
			continue
		}
		if match != nil {
			return fmt.Errorf("multiple mappings match %s %s", network, wslAddress)
		}
		match = mapping
	}
	if match == nil {
		return fmt.Errorf("no mapping matches %s %s", network, wslAddress)
	}
	if match.State != "active" {
		if match.RetryAt != "" {
			return fmt.Errorf("mapping is rejected: %s (retry at %s)", match.Error, match.RetryAt)
		}
		return fmt.Errorf("mapping is rejected: %s", match.Error)
	}
	_, err := fmt.Fprintln(writer, match.WindowsAddress)
	return err
}

func printStatus(writer io.Writer, document autoforward.StatusDocument) error {
	mappings := append([]autoforward.MappingStatus(nil), document.Mappings...)
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].Network != mappings[j].Network {
			return mappings[i].Network < mappings[j].Network
		}
		return mappings[i].WSLAddress < mappings[j].WSLAddress
	})
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "publisher\t%d\tupdated\t%s\n", document.ProcessID, document.UpdatedAt); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "NETWORK\tSTATE\tWINDOWS\tWSL\tDETAIL"); err != nil {
		return err
	}
	for _, mapping := range mappings {
		detail := "-"
		if mapping.Error != "" {
			detail = fmt.Sprintf("%q", mapping.Error)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", mapping.Network, mapping.State, mapping.WindowsAddress, mapping.WSLAddress, detail); err != nil {
			return err
		}
	}
	return table.Flush()
}
