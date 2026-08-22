// Package migrations embeds the PostgreSQL control-plane schema.
package migrations

import _ "embed"

//go:embed 000001_control_plane.up.sql
var up string

//go:embed 000001_control_plane.down.sql
var down string

// Up returns SQL that creates or verifies the schema.
func Up() string { return up }

// Down returns destructive SQL intended only for tests and disaster recovery.
func Down() string { return down }
