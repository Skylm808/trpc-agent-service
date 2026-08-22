// Package migrations embeds the PostgreSQL control-plane schema.
package migrations

import _ "embed"

//go:embed 000001_control_plane.up.sql
var up string

//go:embed 000001_control_plane.down.sql
var down string

//go:embed 000002_message_runtime.up.sql
var messageRuntimeUp string

//go:embed 000002_message_runtime.down.sql
var messageRuntimeDown string

// Up returns SQL that creates or verifies the schema.
func Up() string { return up + "\n" + messageRuntimeUp }

// Down returns destructive SQL intended only for tests and disaster recovery.
func Down() string { return messageRuntimeDown + "\n" + down }
