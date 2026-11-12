package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func runDesktopNotice(args []string) {
	if err := sendDesktopNotice(args); err != nil {
		fmt.Fprintln(os.Stderr, "Desktop notice:", err)
		os.Exit(1)
	}
}

func sendDesktopNotice(args []string) error {
	flags := flag.NewFlagSet("desktop-notice", flag.ContinueOnError)
	project := flags.String("project", "", "project")
	session := flags.String("session", "", "bound session")
	dataDir := flags.String("data-dir", "", "cc-connect data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *project == "" || *session == "" {
		return fmt.Errorf("project and session required")
	}
	var notice core.DesktopNotification
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 128<<10)).Decode(&notice); err != nil {
		return fmt.Errorf("invalid notice JSON: %w", err)
	}
	body, err := json.Marshal(map[string]any{"project": *project, "session_key": *session, "notice": notice})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", resolveSocketPath(*dataDir))
	}}}
	response, err := client.Post("http://unix/desktop-notification", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("send desktop notice: %w", err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("desktop notice: %s", result)
	}
	_, err = os.Stdout.Write(result)
	return err
}
