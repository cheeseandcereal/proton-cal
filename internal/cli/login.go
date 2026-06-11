package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal-go/internal/auth"
)

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Proton and save the session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := auth.Login(cmd.Context(), auth.NewTerminalPrompter()); err != nil {
				return err
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

func init() {
	rootCmd.AddCommand(newLoginCmd(), newLogoutCmd())
}
