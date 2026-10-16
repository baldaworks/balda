package proxyruntime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

const BindingEnv = "BALDA_HOOK_BINDING"
const ExecutableEnv = "BALDA_HOOK_EXECUTABLE"

// Protocol owns a provider's native command and ACP wire behavior.
type Protocol interface {
	Name() string
	Dispatch(ctx context.Context, binding hookprovider.Binding, input io.Reader, output io.Writer) error
	Proxy(ctx context.Context, binding hookprovider.Binding, path string, argv []string, input io.Reader, output, stderr io.Writer) error
}

// RunCommandMode executes the private native-adapter subprocess surface.
func RunCommandMode(args []string, protocols ...Protocol) (bool, int) {
	if len(args) == 0 || args[0] != "agent-hooks" {
		return false, 0
	}
	byName := make(map[string]Protocol, len(protocols))
	for _, protocol := range protocols {
		byName[protocol.Name()] = protocol
	}
	var err error
	switch {
	case len(args) == 3 && args[1] == "ready":
		if !matchesAdapter(os.Getenv(BindingEnv), args[2]) {
			err = hookcmd.ErrUnavailable
			break
		}
		err = withBinding(os.Getenv(BindingEnv), func(binding hookprovider.Binding) error {
			if err := binding.SignalReady(context.Background()); err != nil {
				return err
			}
			_, err := io.WriteString(os.Stdout, "{}\n")
			return err
		})
	case len(args) == 3 && args[1] == "dispatch":
		protocol := byName[args[2]]
		if protocol == nil || !matchesAdapter(os.Getenv(BindingEnv), args[2]) {
			err = hookcmd.ErrUnavailable
			break
		}
		err = withBinding(os.Getenv(BindingEnv), func(binding hookprovider.Binding) error {
			return protocol.Dispatch(context.Background(), binding, os.Stdin, os.Stdout)
		})
	case len(args) >= 7 && args[1] == "proxy" && args[5] == "--":
		protocol := byName[args[2]]
		if protocol == nil || !matchesAdapter(args[3], args[2]) {
			err = hookcmd.ErrUnavailable
			break
		}
		proof, proofErr := os.ReadFile(args[3] + ".owner-proof")
		if proofErr != nil || string(proof) != args[4] {
			err = hookcmd.ErrUnavailable
			break
		}
		err = withBinding(args[3], func(binding hookprovider.Binding) error {
			return protocol.Proxy(context.Background(), binding, args[3], args[6:], os.Stdin, os.Stdout, os.Stderr)
		})
	default:
		err = hookcmd.ErrInvalid
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Balda tool hook unavailable; invocation rejected.")
		return true, 2
	}
	return true, 0
}

func matchesAdapter(path, want string) bool {
	value, err := os.ReadFile(path + ".adapter")
	return err == nil && string(value) == want
}

func withBinding(path string, run func(hookprovider.Binding) error) error {
	binding, err := hookprovider.ReadBinding(path)
	if err != nil {
		return err
	}
	return run(binding)
}
