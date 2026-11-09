package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/iml885203/lodog/internal/skills"
)

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the agent skills that drive lodog for a person",
	}
	cmd.AddCommand(newSkillInstallCmd(), newSkillShowCmd())
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	var dir string
	var global, force bool
	cmd := &cobra.Command{
		Use:   "install [skill]",
		Short: "Write a skill where Claude Code finds it (default: lodog-onboard into ./.claude/skills)",
		Long: `Writes a skill this lodog carries to <dir>/<skill>/SKILL.md, where Claude Code
loads skills from: ./.claude/skills for the current project, or
~/.claude/skills with --global for every project. The skill matches this
binary's commands, so install it again after upgrading lodog. A SKILL.md that
was edited is kept unless --force. Skills: ` + fmt.Sprint(skills.Names()) + `.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "lodog-onboard"
			if len(args) == 1 {
				name = args[0]
			}
			target, err := skills.Dir(dir, global)
			if err != nil {
				return err
			}
			path, err := skills.Install(target, name, force)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "installed %s; in Claude Code say \"connect <system> to lodog\" or run /%s\n", path, name)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "skills directory to write into (default ./.claude/skills)")
	f.BoolVar(&global, "global", false, "install into ~/.claude/skills for every project")
	f.BoolVar(&force, "force", false, "replace a SKILL.md that differs from this lodog's")
	return cmd
}

func newSkillShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [skill]",
		Short: "Print a skill's SKILL.md (default lodog-onboard)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "lodog-onboard"
			if len(args) == 1 {
				name = args[0]
			}
			data, err := skills.Read(name)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}
