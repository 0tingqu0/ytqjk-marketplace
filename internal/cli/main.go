package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
)

type commandContext struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

func Main(arguments []string, input io.Reader, output, errorOutput io.Writer) int {
	context := commandContext{in: input, out: output, errOut: errorOutput}
	if len(arguments) == 0 {
		return context.install(arguments)
	}
	if arguments[0] == "--help" || arguments[0] == "-h" {
		_, err := fmt.Fprint(output, helpText)
		if err != nil {
			writeFailure(output, err)
			return 1
		}
		return 0
	}
	if strings.HasPrefix(arguments[0], "-") {
		return context.install(arguments)
	}
	var err error
	switch arguments[0] {
	case "install":
		return context.install(arguments[1:])
	case "uninstall":
		return context.install(append([]string{"--uninstall"}, arguments[1:]...))
	case "rag":
		err = context.rag(arguments[1:])
	case "session":
		err = context.session(arguments[1:])
	case "knowledge":
		err = context.knowledge(arguments[1:])
	case "dashboard":
		err = context.dashboard(arguments[1:])
	case "orchestration":
		err = context.orchestration(arguments[1:])
	case "handoff":
		err = context.handoff(arguments[1:])
	case "upgrade":
		err = context.upgrade(arguments[1:])
	case "hook":
		err = context.hook(arguments[1:])
	case "version", "--version", "-version":
		_, err = fmt.Fprintln(output, buildinfo.Version)
	case "help", "--help", "-h":
		_, err = fmt.Fprint(output, helpText)
	default:
		err = fmt.Errorf("unknown command %q", arguments[0])
	}
	if err != nil {
		writeFailure(output, err)
		return 1
	}
	return 0
}

func (context commandContext) write(value any) error {
	encoder := json.NewEncoder(context.out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeFailure(output io.Writer, err error) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(map[string]any{"ok": false, "error": safeError(err)})
}

func safeError(err error) string {
	if err == nil {
		return "unknown error"
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return "operation failed"
	}
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

func requireCommand(arguments []string, choices ...string) (string, []string, error) {
	if len(arguments) == 0 {
		return "", nil, errors.New("a subcommand is required")
	}
	for _, choice := range choices {
		if arguments[0] == choice {
			return choice, arguments[1:], nil
		}
	}
	return "", nil, fmt.Errorf("unsupported subcommand %q", arguments[0])
}

func requireNoPositionals(arguments []string) error {
	if len(arguments) != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(arguments, " "))
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type stringsFlag []string

func (values *stringsFlag) String() string { return strings.Join(*values, ",") }
func (values *stringsFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

const helpText = `YTQJK local orchestration and knowledge runtime (Go)

Usage:
  ytqjk install [options]
  ytqjk uninstall [options]
  ytqjk rag <init|index|index-global|bootstrap|query> [options]
  ytqjk session <query|anchor|checkpoint|inspect|prepare-archive|finalize-archive> [options]
  ytqjk knowledge <create-project|create-candidate|edit|delete|state|snapshot|feedback|search|intake|workbench> [options]
  ytqjk dashboard <serve|start|stop|status|restart> [options]
  ytqjk orchestration <start-run|show-run|transition|grant|attest|verify> [options]
  ytqjk handoff <export|apply> [options]
  ytqjk upgrade <check|apply|status|rollback|schema-version> [options]
  ytqjk version
`
