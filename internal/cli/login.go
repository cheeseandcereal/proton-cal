package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Proton and save the session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			username, err := auth.Login(cmd.Context(), auth.NewTerminalPrompter(), cfg)
			if err != nil {
				return err
			}
			// Remember the username and (on first login) the detected
			// timezone for subsequent commands.
			cfg.Username = username
			if cfg.Timezone == "" {
				cfg.Timezone = config.DetectTimezone()
			}
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Fprintln(humanOut(), "Logged in successfully.")
			return nil
		},
	}
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear the saved session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := auth.Logout(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(humanOut(), "Session cleared.")
			return nil
		},
	}
}
