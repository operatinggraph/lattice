package output

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultTimeout is the per-operation NATS timeout used by CLI subcommands.
const DefaultTimeout = 10 * time.Second

// Connect opens a substrate connection to the given NATS URL with the
// standard CLI connection name. NATS_NKEY / NATS_CREDS (at most one set)
// supply the transport-authorization credential; both empty ⇒ anonymous.
//
// Every subcommand already wraps its own NATS calls in a DefaultTimeout
// deadline, so a stalled connection fails the command loudly on its own
// terms rather than needing a reconnect budget to save it — MaxReconnects
// is set to the smallest value substrate.Connect can actually express as
// "don't linger": substrate only threads the field into nats.go's options
// when it is nonzero (internal/substrate/conn.go), so a literal 0 here
// would silently fall back to nats.go's own default (60 attempts, ~2s
// apart) instead of failing fast. 1 lets a single transient blip
// self-heal without meaningfully delaying a command that is about to time
// out anyway.
func Connect(ctx context.Context, natsURL string) (*substrate.Conn, error) {
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:           natsURL,
		Name:          "lattice-cli",
		MaxReconnects: 1,
		NKeySeedFile:  os.Getenv("NATS_NKEY"),
		CredsFile:     os.Getenv("NATS_CREDS"),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}
	return conn, nil
}
