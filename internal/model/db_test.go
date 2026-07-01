package model

import (
	"testing"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	if DB != nil {
		sqlDB, err := DB.DB()
		if err == nil {
			sqlDB.Close()
		}
	}
	if err := InitTestDB("file::memory:"); err != nil {
		t.Fatalf("InitTestDB failed: %v", err)
	}
}

func TestIsSetupRequired(t *testing.T) {
	setupTestDB(t)
	if !IsSetupRequired() {
		t.Error("IsSetupRequired should return true initially")
	}
}

func TestSetupAdmin(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	if IsSetupRequired() {
		t.Error("IsSetupRequired should return false after setup")
	}
}

func TestSetupAdminAlreadyExists(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	if err := SetupAdmin("admin2", "password456"); err == nil {
		t.Error("SetupAdmin should return error when admin already exists")
	}
}

func TestSetupAdminEmptyUsername(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("", "password123"); err == nil {
		t.Error("SetupAdmin should return error for empty username")
	}
	if err := SetupAdmin("   ", "password123"); err == nil {
		t.Error("SetupAdmin should return error for whitespace-only username")
	}
}

func TestSetupAdminWeakPassword(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "12345"); err == nil {
		t.Error("SetupAdmin should return error for weak password (too short)")
	}
}

func TestVerifyPassword(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	u, ok := VerifyPassword("admin", "password123")
	if !ok {
		t.Error("VerifyPassword should succeed with correct password")
	}
	if u == nil || u.Username != "admin" {
		t.Error("VerifyPassword should return correct user")
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	if _, ok := VerifyPassword("admin", "wrongpassword"); ok {
		t.Error("VerifyPassword should fail with wrong password")
	}
	if _, ok := VerifyPassword("nonexistent", "password123"); ok {
		t.Error("VerifyPassword should fail for non-existent user")
	}
}

func TestVerifyPasswordByID(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	u, ok := VerifyPassword("admin", "password123")
	if !ok {
		t.Fatalf("VerifyPassword failed: %v", ok)
	}
	u2, ok := VerifyPasswordByID(u.ID, "password123")
	if !ok {
		t.Error("VerifyPasswordByID should succeed with correct password")
	}
	if u2 == nil || u2.ID != u.ID {
		t.Error("VerifyPasswordByID should return correct user")
	}
	if _, ok := VerifyPasswordByID(u.ID, "wrongpassword"); ok {
		t.Error("VerifyPasswordByID should fail with wrong password")
	}
	if _, ok := VerifyPasswordByID(99999, "password123"); ok {
		t.Error("VerifyPasswordByID should fail for non-existent user")
	}
}

func TestUpdatePassword(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	u, _ := VerifyPassword("admin", "password123")
	initialVersion, err := GetUserTokenVersion(u.ID)
	if err != nil {
		t.Fatalf("GetUserTokenVersion failed: %v", err)
	}
	if err := UpdatePassword(u.ID, "newpassword456"); err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}
	newVersion, err := GetUserTokenVersion(u.ID)
	if err != nil {
		t.Fatalf("GetUserTokenVersion after update failed: %v", err)
	}
	if newVersion != initialVersion+1 {
		t.Errorf("token_version should increment by 1, got %d -> %d", initialVersion, newVersion)
	}
	if _, ok := VerifyPassword("admin", "password123"); ok {
		t.Error("Old password should not work after update")
	}
	if _, ok := VerifyPassword("admin", "newpassword456"); !ok {
		t.Error("New password should work after update")
	}
}

func TestUpdatePasswordNonExistent(t *testing.T) {
	setupTestDB(t)
	if err := UpdatePassword(99999, "newpassword456"); err == nil {
		t.Error("UpdatePassword should return error for non-existent user")
	}
}

func TestGetUserTokenVersion(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	u, _ := VerifyPassword("admin", "password123")
	version, err := GetUserTokenVersion(u.ID)
	if err != nil {
		t.Fatalf("GetUserTokenVersion failed: %v", err)
	}
	if version != 0 {
		t.Errorf("Initial token_version should be 0, got %d", version)
	}
}

func TestGetUserByID(t *testing.T) {
	setupTestDB(t)
	if err := SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	u, _ := VerifyPassword("admin", "password123")
	u2, err := GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if u2 == nil || u2.ID != u.ID || u2.Username != "admin" {
		t.Error("GetUserByID should return correct user")
	}
	if _, err := GetUserByID(99999); err == nil {
		t.Error("GetUserByID should return error for non-existent user")
	}
}
