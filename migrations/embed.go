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

//go:embed 000003_persistent_runtime.up.sql
var persistentRuntimeUp string

//go:embed 000003_persistent_runtime.down.sql
var persistentRuntimeDown string

//go:embed 000004_inbox_recovery.up.sql
var inboxRecoveryUp string

//go:embed 000004_inbox_recovery.down.sql
var inboxRecoveryDown string

//go:embed 000005_outbox_delivery.up.sql
var outboxDeliveryUp string

//go:embed 000005_outbox_delivery.down.sql
var outboxDeliveryDown string

//go:embed 000006_cluster_control.up.sql
var clusterControlUp string

//go:embed 000006_cluster_control.down.sql
var clusterControlDown string

//go:embed 000007_storage_migrations.up.sql
var storageMigrationsUp string

//go:embed 000007_storage_migrations.down.sql
var storageMigrationsDown string

//go:embed 000008_pr23_migration_catalog.up.sql
var pr23MigrationCatalogUp string

//go:embed 000008_pr23_migration_catalog.down.sql
var pr23MigrationCatalogDown string

// Up returns SQL that creates or verifies the schema.
func Up() string {
	return up + "\n" + messageRuntimeUp + "\n" + persistentRuntimeUp + "\n" + inboxRecoveryUp + "\n" + outboxDeliveryUp + "\n" + clusterControlUp + "\n" + storageMigrationsUp + "\n" + pr23MigrationCatalogUp
}

// Down returns destructive SQL intended only for tests and disaster recovery.
func Down() string {
	return pr23MigrationCatalogDown + "\n" + storageMigrationsDown + "\n" + clusterControlDown + "\n" + outboxDeliveryDown + "\n" + inboxRecoveryDown + "\n" + persistentRuntimeDown + "\n" + messageRuntimeDown + "\n" + down
}
