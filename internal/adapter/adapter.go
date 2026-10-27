// Package adapter defines the boundary between Pear's planner and the
// mechanisms macOS provides for reading and writing preferences.
package adapter

import (
	"context"

	"github.com/metru/pear/internal/catalog"
)

// Adapter reads, writes, deletes, and applies a single macOS setting.
type Adapter interface {
	// Supports reports whether the adapter can handle a catalog scope.
	Supports(scope string) bool
	// Read returns the current value and whether it exists.
	Read(ctx context.Context, setting catalog.Setting) (value any, found bool, err error)
	// Write sets a value.
	Write(ctx context.Context, setting catalog.Setting, value any) error
	// Delete removes a value.
	Delete(ctx context.Context, setting catalog.Setting) error
	// Restart restarts an application or service by catalog name.
	Restart(ctx context.Context, target string) error
}
