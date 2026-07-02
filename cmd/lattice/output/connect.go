package output

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// DefaultTimeout is the per-operation NATS timeout used by CLI subcommands.
const DefaultTimeout = 10 * time.Second

// Connect opens a substrate connection to the given NATS URL with the
// standard CLI connection name. NATS_NKEY / NATS_CREDS (at most one set)
// supply the transport-authorization credential; both empty ⇒ anonymous.
func Connect(ctx context.Context, natsURL string) (*substrate.Conn, error) {
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "lattice-cli",
		NKeySeedFile: os.Getenv("NATS_NKEY"),
		CredsFile:    os.Getenv("NATS_CREDS"),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}
	return conn, nil
}
