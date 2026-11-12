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

// Internal notification transport: question data is JSON stdin, never shell text.
func runDesktopCard(args []string) {
	if err := sendDesktopCard(args); err != nil {
		fmt.Fprintln(os.Stderr, "Question card:", err)
		os.Exit(1)
	}
}
func sendDesktopCard(args []string) error {
	flags := flag.NewFlagSet("desktop-card", flag.ContinueOnError)
	project := flags.String("project", "", "project")
	session := flags.String("session", "", "bound Feishu session")
	dataDir := flags.String("data-dir", "", "cc-connect data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *project == "" || *session == "" {
		return fmt.Errorf("project and session required")
	}
	var question core.DesktopQuestionCard
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 128<<10)).Decode(&question); err != nil {
		return fmt.Errorf("invalid question JSON")
	}
	if err := question.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"project": *project, "session_key": *session, "question": question})
	if err != nil {
		return err
	}
	socket := resolveSocketPath(*dataDir)
	client := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Post("http://unix/desktop-question", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", string(result))
	}
	_, err = os.Stdout.Write(result)
	return err
}
