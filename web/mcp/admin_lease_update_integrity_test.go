package mcp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Diagnostic regression: exercise the same policy writes with a real WAL file,
// including migration of a pre-AllNodes schema and reopening the database.
func TestSaveLeasePolicyPreservesFileWALIndexes(t *testing.T) {
	for _, migrateLegacy := range []bool{false, true} {
		name := "current-schema"
		if migrateLegacy {
			name = "legacy-all-nodes-migration"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lite.db")
			open := func() *gorm.DB {
				db, err := gorm.Open(sqlite.Open(path+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				if err != nil {
					t.Fatal(err)
				}
				pool, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				pool.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = pool.Close() })
				return db
			}
			db := open()
			if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPToken{}); err != nil {
				t.Fatal(err)
			}
			if migrateLegacy {
				if err := db.Exec("ALTER TABLE mcp_leases DROP COLUMN all_nodes").Error; err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			for i := 0; i < 6; i++ {
				lease := longTermTestLease(string(rune('a'+i)), true, now)
				if err := db.Omit("AllNodes").Create(&lease).Error; err != nil {
					t.Fatal(err)
				}
				for _, kind := range []string{kindRefresh, kindAccess} {
					token := models.MCPToken{Hash: lease.ID + "-" + kind, LeaseID: lease.ID, FamilyID: lease.TokenFamilyID, Kind: kind, ExpiresAt: lease.ExpiresAt}
					if err := db.Create(&token).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			assertLeasePolicySQLiteIntegrity(t, db)
			if err := db.AutoMigrate(&models.MCPLease{}); err != nil {
				t.Fatal(err)
			}
			assertLeasePolicySQLiteIntegrity(t, db)
			for i := 0; i < 240; i++ {
				var lease models.MCPLease
				id := string(rune('a' + i%6))
				if err := db.Where("id = ?", id).First(&lease).Error; err != nil {
					t.Fatal(err)
				}
				lease.AllNodes = (i/6)%2 == 0
				lease.LongTerm = (i/6)%3 == 0
				lease.TargetUUIDs = `["node-a"]`
				if lease.AllNodes {
					lease.TargetUUIDs = "[]"
				}
				lease.ExpiresAt = now.Add(time.Duration(i+1) * time.Minute)
				if lease.LongTerm {
					lease.ExpiresAt = longTermExpiresAt()
				}
				lease.Note = "integrity investigation"
				if err := saveLeasePolicy(db, lease, now); err != nil {
					t.Fatalf("update %d: %v", i, err)
				}
				var stored models.MCPLease
				if err := db.Where("id = ?", id).First(&stored).Error; err != nil {
					t.Fatal(err)
				}
				if stored.AllNodes != lease.AllNodes || stored.LongTerm != lease.LongTerm ||
					stored.TargetUUIDs != lease.TargetUUIDs || !stored.ExpiresAt.Equal(lease.ExpiresAt) {
					t.Fatalf("update %d did not preserve policy: %+v", i, stored)
				}
				var refresh models.MCPToken
				if err := db.Where("hash = ?", id+"-"+kindRefresh).First(&refresh).Error; err != nil {
					t.Fatal(err)
				}
				if !refresh.ExpiresAt.Equal(lease.ExpiresAt) {
					t.Fatalf("update %d refresh expiry did not follow lease", i)
				}
				assertLeasePolicySQLiteIntegrity(t, db)
				if i%40 == 39 {
					pool, _ := db.DB()
					if err := pool.Close(); err != nil {
						t.Fatal(err)
					}
					db = open()
					assertLeasePolicySQLiteIntegrity(t, db)
				}
			}
		})
	}
}

func assertLeasePolicySQLiteIntegrity(t *testing.T, db *gorm.DB) {
	t.Helper()
	var checks []string
	if err := db.Raw("PRAGMA integrity_check").Scan(&checks).Error; err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0] != "ok" {
		t.Fatalf("integrity check: %v", checks)
	}
	var indexed, unindexed int
	if err := db.Raw("SELECT count(*) FROM mcp_leases INDEXED BY idx_mcp_leases_expires_at").Scan(&indexed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT count(*) FROM mcp_leases NOT INDEXED").Scan(&unindexed).Error; err != nil {
		t.Fatal(err)
	}
	if indexed != unindexed {
		t.Fatalf("expiry index count %d != table count %d", indexed, unindexed)
	}
}
