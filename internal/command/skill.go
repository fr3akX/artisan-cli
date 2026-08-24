package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/fr3akX/artisan-cli/internal/output"
	embeddedskill "github.com/fr3akX/artisan-cli/internal/skill"
)

const (
	skillUsage        = "Usage: artisan skill list|show|install\n"
	skillListUsage    = "Usage: artisan skill list\n"
	skillShowUsage    = "Usage: artisan skill show [NAME]\n"
	skillInstallUsage = "Usage: artisan skill install [NAME] --directory ROOT [--force]\n"
)

type skillShowResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type skillListResult struct {
	Names []string `json:"names"`
}

var installEmbeddedSkill = embeddedskill.Install

func runSkill(_ context.Context, args []string, runtime Runtime, jsonMode bool) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeCommandHelp(runtime, jsonMode, skillUsage)
	}
	if len(args) == 0 {
		return skillUsageFailure(runtime, jsonMode, "A skill command is required")
	}
	switch args[0] {
	case "list":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return writeCommandHelp(runtime, jsonMode, skillListUsage)
		}
		if len(args) != 1 {
			return skillUsageFailure(runtime, jsonMode, "skill list does not accept arguments")
		}
		names := embeddedskill.Names()
		if err := output.WriteSuccess(runtime.Out, jsonMode, skillListResult{Names: names}, func(w io.Writer) error {
			for _, name := range names {
				if _, err := fmt.Fprintln(w, name); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return reportWriteError(runtime.Err, err)
		}
		return 0
	case "show":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return writeCommandHelp(runtime, jsonMode, skillShowUsage)
		}
		if len(args) > 2 {
			return skillUsageFailure(runtime, jsonMode, "skill show accepts at most one NAME")
		}
		name := embeddedskill.DefaultName
		if len(args) == 2 {
			name = args[1]
		}
		definition, ok := embeddedskill.Lookup(name)
		if !ok {
			return writeFailure(runtime, jsonMode, unknownSkillFailure())
		}
		result := skillShowResult{Name: definition.Name, Content: string(definition.Content)}
		if err := output.WriteSuccess(runtime.Out, jsonMode, result, func(w io.Writer) error {
			_, err := w.Write(definition.Content)
			return err
		}); err != nil {
			return reportWriteError(runtime.Err, err)
		}
		return 0
	case "install":
		return runSkillInstall(args[1:], runtime, jsonMode)
	default:
		return skillUsageFailure(runtime, jsonMode, "Unknown skill command")
	}
}

func runSkillInstall(args []string, runtime Runtime, jsonMode bool) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeCommandHelp(runtime, jsonMode, skillInstallUsage)
	}
	flags := flag.NewFlagSet("artisan skill install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("directory", "", "agent skill root")
	force := flags.Bool("force", false, "replace differing content")
	if err := flags.Parse(args); err != nil || len(flags.Args()) > 1 || *directory == "" {
		return skillUsageFailure(runtime, jsonMode, "skill install requires optional NAME and --directory ROOT")
	}
	name := embeddedskill.DefaultName
	if len(flags.Args()) == 1 {
		name = flags.Args()[0]
	}
	if _, ok := embeddedskill.Lookup(name); !ok {
		return writeFailure(runtime, jsonMode, unknownSkillFailure())
	}
	result, err := installEmbeddedSkill(*directory, name, *force)
	if err != nil {
		return writeFailure(runtime, jsonMode, skillInstallFailure(err))
	}
	if err := output.WriteSuccess(runtime.Out, jsonMode, result, func(w io.Writer) error {
		status := "Installed"
		if result.Unchanged {
			status = "Already installed"
		}
		_, err := fmt.Fprintf(w, "%s %s at %s\n", status, name, output.EscapeVisible(result.Path))
		return err
	}); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}

func unknownSkillFailure() output.Error {
	return output.Error{ExitCode: usageExitCode, Code: "unknown_skill", Message: "Unknown embedded skill"}
}

func skillInstallFailure(err error) output.Error {
	switch {
	case errors.Is(err, embeddedskill.ErrUnknownSkill):
		return unknownSkillFailure()
	case errors.Is(err, embeddedskill.ErrInstallLocationChanged):
		return output.Error{ExitCode: 3, Code: "skill_install_location_changed", Message: "Skill installed location changed during installation; inspect before retrying"}
	case embeddedskill.InstallVisible(err):
		return output.Error{ExitCode: 3, Code: "skill_install_durability_unknown", Message: "The skill became visible but directory durability is uncertain; inspect it before retrying"}
	case errors.Is(err, embeddedskill.ErrInvalidDirectory):
		return output.Error{ExitCode: usageExitCode, Code: "invalid_skill_directory", Message: "Skill directory must be an existing safe directory without parent traversal"}
	case errors.Is(err, embeddedskill.ErrDifferentContent):
		return output.Error{ExitCode: 3, Code: "skill_exists", Message: "Installed skill differs; use --force to replace it"}
	case errors.Is(err, embeddedskill.ErrUnsafeTarget):
		return output.Error{ExitCode: 3, Code: "unsafe_skill_target", Message: "Skill install target is unsafe"}
	default:
		return output.Error{ExitCode: 3, Code: "skill_install_failed", Message: "Unable to install the skill atomically"}
	}
}

func skillUsageFailure(runtime Runtime, jsonMode bool, message string) int {
	return writeFailure(runtime, jsonMode, output.Error{ExitCode: usageExitCode, Code: "usage", Message: message})
}
