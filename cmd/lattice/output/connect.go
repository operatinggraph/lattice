package output

import (
	"context"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// DefaultTimeout is the per-operation NATS timeout used by CLI subcommands.
const DefaultTimeout = 10 * time.Second

// Connect opens a substrate connection to the given NATS URL with the
// standard CLI connection name.
func Connect(ctx context.Context, natsURL string) (*substrate.Conn, error) {
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:  natsURL,
		Name: "lattice-cli",
	})
	if err != nil {
		return nil, fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}
	return conn, nil
}
