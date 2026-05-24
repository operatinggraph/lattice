package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is the Phase 1 hard-coded version string. Phase 2+ will inject
// this via ldflags at build time.
const version = "dev"

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the lattice version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("lattice version %s\n", version)
			return nil
		},
	})

	rootCmd.Version = version
}
