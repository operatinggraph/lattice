// Package query implements the lattice query command group.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
)

// dmlKeywords are the SQL statement prefixes that are considered DML
// (data manipulation language). All DML is rejected — the operator
// should use `lattice op submit` instead.
var dmlKeywords = []string{"INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT",
	"TRUNCATE", "DROP", "CREATE", "ALTER", "REPLACE", "CALL", "EXEC",
	"WITH", "DO"}

// NewCommand returns the cobra.Command for the query command group.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query Postgres (passthrough) and Capability KV",
	}
	cmd.AddCommand(newCapCommand(natsURL, outputFmt))
	cmd.AddCommand(newPostgresCommand(outputFmt))
	return cmd
}

func newCapCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "cap <actorKey>",
		Short: "Read an actor's capability document from Capability KV",
		Long: `cap reads the resolved capability document for the given actor from
Capability KV. The actorKey may be provided as either the full vertex
key (vtx.identity.<NanoID>) or just the NanoID portion.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actorKey := args[0]

			// Derive the Capability KV key from the actor key.
			// Full vertex key: vtx.identity.<NanoID> → cap.identity.<NanoID>
			// Short form: <NanoID> → cap.identity.<NanoID>
			capKey := deriveCapKey(actorKey)

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

			entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, capKey)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("NotFound", fmt.Sprintf("capability key %s: %v", capKey, err))
				}
				fmt.Fprintf(os.Stderr, "not found: %s (%v)\n", capKey, err)
				os.Exit(1)
			}

			if *outputFmt == "json" {
				var doc interface{}
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					return output.PrintJSONError("ParseError", err.Error())
				}
				return output.PrintJSON(doc)
			}
			fmt.Printf("capKey: %s\n%s\n", capKey, string(entry.Value))
			return nil
		},
	}
}

// deriveCapKey converts an actor vertex key to a Capability KV key.
// vtx.identity.<NanoID> → cap.identity.<NanoID>
func deriveCapKey(actorKey string) string {
	if strings.HasPrefix(actorKey, "vtx.identity.") {
		return "cap.identity." + actorKey[len("vtx.identity."):]
	}
	if strings.HasPrefix(actorKey, "cap.identity.") {
		return actorKey
	}
	// Bare NanoID.
	return "cap.identity." + actorKey
}

func newPostgresCommand(outputFmt *string) *cobra.Command {
	var postgresURL string

	cmd := &cobra.Command{
		Use:   "postgres <sql>",
		Short: "Execute a read-only SQL query against Postgres (thin passthrough)",
		Long: `postgres executes a read-only SQL query against the Lattice Postgres
instance. No query rewriting, no ORM semantics, no SQL wrapping.

DML statements (INSERT, UPDATE, DELETE, CREATE, DROP, etc.) are
rejected with an error directing the operator to use lattice op submit.

Phase 1: read-only queries only.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sqlQuery := args[0]

			if err := rejectDML(sqlQuery); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("DMLNotAllowed", err.Error())
				}
				return err
			}

			if postgresURL == "" {
				postgresURL = os.Getenv("POSTGRES_URL")
			}
			if postgresURL == "" {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConfigError", "POSTGRES_URL env or --postgres-url flag is required")
				}
				return fmt.Errorf("POSTGRES_URL env or --postgres-url flag is required")
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := pgx.Connect(ctx, postgresURL)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConnectionError", fmt.Sprintf("connect to Postgres: %v", err))
				}
				return fmt.Errorf("connect to Postgres: %w", err)
			}
			defer conn.Close(ctx)

			rows, err := conn.Query(ctx, sqlQuery)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("QueryError", err.Error())
				}
				return fmt.Errorf("query: %w", err)
			}
			defer rows.Close()

			fieldDescs := rows.FieldDescriptions()
			colNames := make([]string, len(fieldDescs))
			for i, f := range fieldDescs {
				colNames[i] = string(f.Name)
			}

			var results []map[string]interface{}
			for rows.Next() {
				vals, err := rows.Values()
				if err != nil {
					return fmt.Errorf("scan row: %w", err)
				}
				row := make(map[string]interface{}, len(colNames))
				for i, col := range colNames {
					row[col] = vals[i]
				}
				results = append(results, row)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("rows: %w", err)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(results)
			}

			// Print as table.
			if len(results) == 0 {
				fmt.Println("(0 rows)")
				return nil
			}
			// Print header.
			fmt.Println(strings.Join(colNames, "\t"))
			fmt.Println(strings.Repeat("-", 40))
			for _, row := range results {
				vals := make([]string, len(colNames))
				for i, col := range colNames {
					vals[i] = fmt.Sprintf("%v", row[col])
				}
				fmt.Println(strings.Join(vals, "\t"))
			}
			fmt.Printf("(%d rows)\n", len(results))
			return nil
		},
	}

	cmd.Flags().StringVar(&postgresURL, "postgres-url", "", "Postgres connection URL (env: POSTGRES_URL)")
	return cmd
}

// stripLeadingComments removes SQL-style line comments (-- ...) and block
// comments (/* ... */) from the head of the query before keyword matching.
func stripLeadingComments(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		if strings.HasPrefix(s, "--") {
			if i := strings.Index(s, "\n"); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			// Comment runs to end of string — nothing left.
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
				continue
			}
			// Unterminated block comment — nothing left.
			return ""
		}
		break
	}
	return s
}

// rejectDML returns an error if the SQL query begins with a DML keyword,
// after stripping any leading SQL comments.
func rejectDML(sql string) error {
	upper := strings.ToUpper(stripLeadingComments(sql))
	for _, kw := range dmlKeywords {
		if strings.HasPrefix(upper, kw) {
			return fmt.Errorf("DML statement %q is not allowed via lattice query postgres; "+
				"use lattice op submit to modify data through the Processor write path", kw)
		}
	}
	return nil
}
