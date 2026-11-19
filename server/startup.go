package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
)

var startupIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Called only after authenticated device.hello. One image per foreground launch,
// not per reconnect. It deliberately uses normal commands and assignment checks.
func (a *App) queueStartupImage(deviceID, startupID string) error {
	if !startupIDPattern.MatchString(startupID) {
		return nil
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	return a.db.Transaction(func(tx *gorm.DB) error {
		var d Device
		if err := tx.First(&d, "id = ?", deviceID).Error; err != nil {
			return err
		}
		if !d.Enabled {
			return nil
		}
		key := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", d.ID, d.AssignmentID, d.AssignmentVersion, startupID)))
		id := fmt.Sprintf("cmd_boot_%x", key[:20])
		var existing Command
		if err := tx.First(&existing, "id = ?", id).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !a.limiter.Allow("startup-image:"+d.ID, 6, time.Minute) {
			return nil
		}
		parameters, err := json.Marshal(map[string]any{"capture_source": "uikit_window", "capture_scope": "active_window", "allow_cached": false, "max_long_edge_px": 1280, "trigger": "app_launch"})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		c := Command{ID: id, DeviceID: d.ID, AssignmentID: d.AssignmentID, AssignmentVersion: d.AssignmentVersion, Type: "GET_IMAGE", Parameters: string(parameters), Status: "queued", CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
		return tx.Create(&c).Error
	})
}
