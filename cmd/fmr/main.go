// Command fmr is the thin control-plane CLI for Free-Model-Router.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
)

var commandNames = []string{"start", "stop", "status", "models", "providers", "model-pool", "config", "logs", "doctor"}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "fmr: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return nil
	}
	command := args[0]
	if !containsCommand(command) {
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet("fmr "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultAddr := os.Getenv("FMR_CONTROL_ADDR")
	if defaultAddr == "" {
		defaultAddr = "127.0.0.1:8788"
	}
	addr := flags.String("addr", defaultAddr, "control API address")
	token := flags.String("token", os.Getenv(config.ManagementTokenKey), "management bearer token")
	configPath := flags.String("config", "", "gateway config path (start only)")
	jsonOutput := flags.Bool("json", false, "print JSON response without formatting")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *token == "" {
		*token = readStoredToken()
	}
	client := control.NewClient(*addr, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch command {
	case "start":
		if _, err := client.Get(ctx, "/_fmr/status"); err == nil {
			return printText(stdout, "{\"status\":\"already-running\"}\n", *jsonOutput)
		}
		if err := startGateway(*configPath, stderr); err != nil {
			return err
		}
		if err := waitForGateway(ctx, client); err != nil {
			return err
		}
		return printText(stdout, "{\"status\":\"started\"}\n", *jsonOutput)
	case "stop":
		body, err := client.Post(ctx, "/_fmr/stop", nil)
		if err != nil {
			return err
		}
		return printBody(stdout, body, *jsonOutput)
	case "status":
		return getAndPrint(ctx, client, "/_fmr/status", stdout, *jsonOutput)
	case "models":
		return getAndPrint(ctx, client, "/_fmr/models", stdout, *jsonOutput)
	case "providers":
		return getAndPrint(ctx, client, "/_fmr/providers", stdout, *jsonOutput)
	case "model-pool":
		return getAndPrint(ctx, client, "/_fmr/model-pool", stdout, *jsonOutput)
	case "config":
		return getAndPrint(ctx, client, "/_fmr/config", stdout, *jsonOutput)
	case "logs":
		return getAndPrint(ctx, client, "/_fmr/logs", stdout, *jsonOutput)
	case "doctor":
		if err := getAndPrint(ctx, client, "/_fmr/status", stdout, *jsonOutput); err != nil {
			return fmt.Errorf("control API check failed: %w", err)
		}
		return nil
	default:
		return errors.New("unreachable command")
	}
}

func getAndPrint(ctx context.Context, client *control.Client, path string, stdout io.Writer, jsonOutput bool) error {
	body, err := client.Get(ctx, path)
	if err != nil {
		return err
	}
	return printBody(stdout, body, jsonOutput)
}

func printBody(stdout io.Writer, body []byte, jsonOutput bool) error {
	payload := bytes.TrimSpace(body)
	if !jsonOutput {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, payload, "", "  "); err == nil {
			payload = pretty.Bytes()
		}
	}
	output := append(append([]byte(nil), payload...), '\n')
	_, err := stdout.Write(output)
	return err
}

func printText(stdout io.Writer, text string, jsonOutput bool) error {
	return printBody(stdout, []byte(text), jsonOutput)
}

func readStoredToken() string {
	store := config.NewSecretStore()
	token, err := store.Get(config.ManagementTokenKey)
	if err != nil {
		return ""
	}
	return token
}

func startGateway(configPath string, diagnostics io.Writer) error {
	binary, err := gatewayBinaryPath()
	if err != nil {
		return err
	}
	args := make([]string, 0, 2)
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "-config", configPath)
	}
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	command := exec.Command(binary, args...)
	command.Stdout = diagnostics
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	_, _ = fmt.Fprintf(diagnostics, "started gateway pid %d\n", command.Process.Pid)
	return nil
}

func waitForGateway(ctx context.Context, client *control.Client) error {
	if client == nil {
		return errors.New("control client must not be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if _, err := client.Get(ctx, "/_fmr/status"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if strings.TrimSpace(client.Token) == "" {
			if token := readStoredToken(); token != "" {
				client.Token = token
			}
		}
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return fmt.Errorf("gateway did not become ready: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func gatewayBinaryPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve fmr executable: %w", err)
	}
	dir := filepath.Dir(executable)
	candidates := []string{filepath.Join(dir, "Free-Model-Router.exe"), filepath.Join(dir, "Free-Model-Router")}
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("gateway binary not found next to %s", executable)
}

func containsCommand(command string) bool {
	for _, candidate := range commandNames {
		if candidate == command {
			return true
		}
	}
	return false
}

func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, "Usage: fmr <command> [flags]\n\nCommands:\n")
	for _, command := range commandNames {
		_, _ = fmt.Fprintf(w, "  %s\n", command)
	}
	_, _ = io.WriteString(w, "\nFlags: -addr, -token, -config, -json\n")
}
