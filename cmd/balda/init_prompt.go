package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func chooseBaldaProvider(agentIDs []string, in io.Reader, out io.Writer, interactive bool) (string, error) {
	if len(agentIDs) == 0 {
		return "", fmt.Errorf("no provider ids are available for balda.provider selection")
	}
	if !interactive {
		return agentIDs[0], nil
	}
	return promptBaldaProvider(agentIDs, in, out)
}

func promptBaldaProvider(agentIDs []string, in io.Reader, out io.Writer) (string, error) {
	if len(agentIDs) == 0 {
		return "", fmt.Errorf("no provider ids are available for balda.provider selection")
	}

	_, _ = fmt.Fprintln(out, "Select balda.provider:")
	for i, id := range agentIDs {
		_, _ = fmt.Fprintf(out, "  %d) %s\n", i+1, id)
	}
	_, _ = fmt.Fprintf(out, "Enter number or provider id [1]: ")

	reader := asBufferedReader(in)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read balda.provider selection: %w", err)
		}

		value := strings.TrimSpace(line)
		if value == "" {
			return agentIDs[0], nil
		}

		if idx, parseErr := strconv.Atoi(value); parseErr == nil && idx >= 1 && idx <= len(agentIDs) {
			return agentIDs[idx-1], nil
		}

		if contains(agentIDs, value) {
			return value, nil
		}

		if err == io.EOF {
			return "", fmt.Errorf("invalid balda.provider selection %q", value)
		}

		_, _ = fmt.Fprintf(
			out,
			"Invalid selection %q. Enter number 1-%d or one of: %s\n",
			value,
			len(agentIDs),
			strings.Join(agentIDs, ", "),
		)
		_, _ = fmt.Fprintf(out, "Enter number or provider id [1]: ")
	}
}

func promptBaldaTelegramToken(in io.Reader, out io.Writer, interactive bool) (string, botIdentity, error) {
	reader := asBufferedReader(in)
	for {
		_, _ = fmt.Fprint(out, "Enter Telegram bot token (required): ")
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", botIdentity{}, fmt.Errorf("read balda.telegram.token: %w", err)
		}

		token := strings.TrimSpace(line)
		if token == "" {
			if err == io.EOF || !interactive {
				return "", botIdentity{}, fmt.Errorf("balda.telegram.token is required")
			}
			_, _ = fmt.Fprintln(out, "Token is required.")
			continue
		}

		identity, validateErr := baldaInitLoadBotIdentity(context.Background(), token)
		if validateErr == nil {
			return token, identity, nil
		}

		if err == io.EOF || !interactive {
			return "", botIdentity{}, fmt.Errorf("validate balda.telegram.token: %w", validateErr)
		}

		_, _ = fmt.Fprintf(out, "Token validation failed: %v\n", validateErr)
	}
}

func chooseBaldaTelegramTokenStorage(in io.Reader, out io.Writer, interactive bool) (baldaTokenStorageMode, error) {
	if !interactive {
		return baldaTokenStorageEnv, nil
	}

	reader := asBufferedReader(in)
	_, _ = fmt.Fprintln(out, "Where should Telegram token be stored?")
	_, _ = fmt.Fprintln(out, "  1) .env (default)")
	_, _ = fmt.Fprintln(out, "  2) balda config file")
	_, _ = fmt.Fprint(out, "Enter choice [1]: ")

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read telegram token storage selection: %w", err)
		}

		value := strings.ToLower(strings.TrimSpace(line))
		switch value {
		case "", "1", ".env", "env":
			return baldaTokenStorageEnv, nil
		case "2", "config", "config file":
			return baldaTokenStorageConfig, nil
		}

		if err == io.EOF {
			return "", fmt.Errorf("invalid telegram token storage selection %q", value)
		}
		_, _ = fmt.Fprintf(out, "Invalid selection %q. Enter 1 (.env) or 2 (config file).\n", value)
		_, _ = fmt.Fprint(out, "Enter choice [1]: ")
	}
}

func storeBaldaTelegramToken(
	doc map[string]any,
	workingDir string,
	token string,
	mode baldaTokenStorageMode,
) (string, error) {
	switch mode {
	case baldaTokenStorageEnv:
		if err := setBaldaTelegramToken(doc, ""); err != nil {
			return "", err
		}
		dotEnvPath := filepath.Join(workingDir, baldaDotEnvFileName)
		if err := upsertBaldaTelegramTokenEnv(dotEnvPath, token); err != nil {
			return "", err
		}
		return dotEnvPath, nil
	case baldaTokenStorageConfig:
		if err := setBaldaTelegramToken(doc, token); err != nil {
			return "", err
		}
		return filepath.Join(workingDir, baldaConfigRelDir, baldaConfigFileName), nil
	default:
		return "", fmt.Errorf("unsupported telegram token storage mode %q", mode)
	}
}

func upsertBaldaTelegramTokenEnv(dotEnvPath string, token string) error {
	line := "BALDA_TELEGRAM_TOKEN=" + strings.TrimSpace(token)

	content, err := os.ReadFile(dotEnvPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", dotEnvPath, err)
		}
		if writeErr := os.WriteFile(dotEnvPath, []byte(line+"\n"), 0o600); writeErr != nil {
			return fmt.Errorf("write %s: %w", dotEnvPath, writeErr)
		}
		return nil
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	out := make([]string, 0, len(lines))
	replaced := false
	for _, rawLine := range lines {
		if isBaldaTelegramTokenEnvLine(rawLine) {
			if !replaced {
				out = append(out, line)
				replaced = true
			}
			continue
		}
		out = append(out, rawLine)
	}

	if !replaced {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, line)
	}

	updated := strings.Join(out, "\n")
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}

	if err := os.WriteFile(dotEnvPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dotEnvPath, err)
	}
	return nil
}

func isBaldaTelegramTokenEnvLine(rawLine string) bool {
	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	idx := strings.Index(trimmed, "=")
	if idx < 0 {
		return false
	}
	key := strings.TrimSpace(trimmed[:idx])
	return key == "BALDA_TELEGRAM_TOKEN"
}

func asBufferedReader(in io.Reader) *bufio.Reader {
	if reader, ok := in.(*bufio.Reader); ok {
		return reader
	}
	return bufio.NewReader(in)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func defaultBaldaInitIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
