// Package graph implements the lattice graph command group.
//
// WARNING: lattice graph is a debug and operator surface. It is NOT a
// sanctioned client read path (architecture rule: Adjacency KV is
// Refractor-private; all production reads must go through a Lens).
// Do not use these commands in client applications or scripts.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
)

const debugWarning = `WARNING: lattice graph is a debug and operator surface. It is NOT a
sanctioned client read path (architecture rule: Adjacency KV is
Refractor-private; all production reads must go through a Lens).
Do not use these commands in client applications or scripts.`

// NewCommand returns the cobra.Command for the graph command group.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Debug/operator direct Core KV reads (NOT a sanctioned client path)",
		Long:  debugWarning,
	}
	cmd.AddCommand(newReadCommand(natsURL, outputFmt))
	cmd.AddCommand(newWalkCommand(natsURL, outputFmt))
	cmd.AddCommand(newKeysCommand(natsURL, outputFmt))
	return cmd
}

func newReadCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "read <key>",
		Short: "Read a Core KV key directly (debug only)",
		Long: debugWarning + `

read retrieves the raw JSON value of a Core KV key. Returns non-zero
if the key is missing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("NotFound", fmt.Sprintf("key %s: %v", key, err))
				}
				return fmt.Errorf("key not found: %s: %w", key, err)
			}

			if *outputFmt == "json" {
				var doc interface{}
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					return output.PrintJSONError("ParseError", err.Error())
				}
				return output.PrintJSON(doc)
			}
			fmt.Printf("%s\n", string(entry.Value))
			return nil
		},
	}
}

func newWalkCommand(natsURL, outputFmt *string) *cobra.Command {
	var depth int

	cmd := &cobra.Command{
		Use:   "walk <startKey> [--depth N]",
		Short: "Walk connected vertices in Core KV (debug only)",
		Long: debugWarning + `

walk enumerates the vertex keys connected to startKey by listing
Core KV keys with the prefix <startKey>. traversal is BFS up to
the specified depth (default 3, max 10).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			startKey := args[0]
			if depth < 1 {
				depth = 3
			}
			if depth > 10 {
				depth = 10
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			visited := map[string]bool{}
			var result []string

			var bfs func(key string, d int)
			bfs = func(key string, d int) {
				if d <= 0 || visited[key] {
					return
				}
				visited[key] = true
				result = append(result, key)

				allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
				if err != nil {
					return
				}
				prefix := key + "."
				for _, k := range allKeys {
					if strings.HasPrefix(k, prefix) {
						bfs(k, d-1)
					}
				}
			}

			bfs(startKey, depth)

			if *outputFmt == "json" {
				return output.PrintJSON(map[string]interface{}{
					"startKey": startKey,
					"depth":    depth,
					"keys":     result,
				})
			}
			fmt.Printf("walk from %s (depth=%d):\n", startKey, depth)
			for _, k := range result {
				fmt.Printf("  %s\n", k)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&depth, "depth", 3, "traversal depth (max 10)")
	return cmd
}

func newKeysCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "keys [<prefix>]",
		Short: "List Core KV keys matching a prefix (debug only)",
		Long: debugWarning + `

keys lists all keys in Core KV matching the given prefix.
With no prefix, lists all keys (use with caution on large graphs).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) == 1 {
				prefix = args[0]
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list keys: %w", err)
			}

			var matched []string
			for _, k := range allKeys {
				if prefix == "" || strings.HasPrefix(k, prefix) {
					matched = append(matched, k)
				}
			}

			if *outputFmt == "json" {
				return output.PrintJSON(map[string]interface{}{
					"prefix": prefix,
					"keys":   matched,
					"count":  len(matched),
				})
			}
			for _, k := range matched {
				fmt.Println(k)
			}
			return nil
		},
	}
}
