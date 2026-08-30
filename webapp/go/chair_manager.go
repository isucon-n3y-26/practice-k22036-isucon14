package main

import (
	"context"
	"math"
	"sync"

	"github.com/jmoiron/sqlx"
)

type ChairState struct {
	ID                   string
	Name                 string
	Model                string
	Speed                int
	IsActive             bool
	Latitude             int
	Longitude            int
	HasLocation          bool
	CurrentRideID        string
}

type ChairManager struct {
	mu          sync.RWMutex
	chairs      map[string]*ChairState
	modelSpeeds map[string]int
}

var globalChairManager = &ChairManager{
	chairs:      make(map[string]*ChairState),
	modelSpeeds: make(map[string]int),
}

func (cm *ChairManager) Reload(ctx context.Context, db *sqlx.DB) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.chairs = make(map[string]*ChairState)
	cm.modelSpeeds = make(map[string]int)

	// 1. モデル速度のロード
	type modelRow struct {
		Name  string `db:"name"`
		Speed int    `db:"speed"`
	}
	var models []modelRow
	if err := db.SelectContext(ctx, &models, "SELECT name, speed FROM chair_models"); err != nil {
		return err
	}
	for _, m := range models {
		cm.modelSpeeds[m.Name] = m.Speed
	}

	// 2. 椅子情報のロード
	type chairRow struct {
		ID       string `db:"id"`
		Name     string `db:"name"`
		Model    string `db:"model"`
		IsActive bool   `db:"is_active"`
	}
	var chairs []chairRow
	if err := db.SelectContext(ctx, &chairs, "SELECT id, name, model, is_active FROM chairs"); err != nil {
		return err
	}
	for _, c := range chairs {
		speed := cm.modelSpeeds[c.Model]
		if speed == 0 {
			speed = 2
		}
		cm.chairs[c.ID] = &ChairState{
			ID:                      c.ID,
			Name:                    c.Name,
			Model:                   c.Model,
			Speed:                   speed,
			IsActive:                c.IsActive,
		}
	}

	// 3. 最新位置情報のロード
	type locRow struct {
		ChairID   string `db:"chair_id"`
		Latitude  int    `db:"latitude"`
		Longitude int    `db:"longitude"`
	}
	var locs []locRow
	if err := db.SelectContext(ctx, &locs, `
		SELECT chair_id, latitude, longitude
		FROM (
			SELECT chair_id, latitude, longitude,
			       ROW_NUMBER() OVER (PARTITION BY chair_id ORDER BY created_at DESC) as rn
			FROM chair_locations
		) t
		WHERE rn = 1
	`); err != nil {
		return err
	}
	for _, l := range locs {
		if state, ok := cm.chairs[l.ChairID]; ok {
			state.Latitude = l.Latitude
			state.Longitude = l.Longitude
			state.HasLocation = true
		}
	}

	// 4. 未完了ライドのロード
	type incompleteRideRow struct {
		ChairID string `db:"chair_id"`
		RideID  string `db:"id"`
	}
	var incompleteRides []incompleteRideRow
	if err := db.SelectContext(ctx, &incompleteRides, `
		SELECT r.id, r.chair_id
		FROM rides r
		JOIN (
			SELECT ride_id, status FROM ride_statuses rs
			WHERE rs.created_at = (SELECT MAX(created_at) FROM ride_statuses WHERE ride_id = rs.ride_id)
		) latest_rs ON latest_rs.ride_id = r.id
		WHERE r.chair_id IS NOT NULL AND latest_rs.status <> 'COMPLETED'
	`); err != nil {
		return err
	}
	for _, r := range incompleteRides {
		if state, ok := cm.chairs[r.ChairID]; ok {
			state.CurrentRideID = r.RideID
		}
	}

	return nil
}

func (cm *ChairManager) RegisterChair(id, name, model string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	speed := cm.modelSpeeds[model]
	if speed == 0 {
		speed = 2
	}
	cm.chairs[id] = &ChairState{
		ID:       id,
		Name:     name,
		Model:    model,
		Speed:    speed,
		IsActive: false,
	}
}

func (cm *ChairManager) SetActivity(chairID string, isActive bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if state, ok := cm.chairs[chairID]; ok {
		state.IsActive = isActive
	}
}

func (cm *ChairManager) UpdateLocation(chairID string, lat, lon int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if state, ok := cm.chairs[chairID]; ok {
		state.Latitude = lat
		state.Longitude = lon
		state.HasLocation = true
	}
}

func (cm *ChairManager) AssignRide(chairID, rideID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if state, ok := cm.chairs[chairID]; ok {
		state.CurrentRideID = rideID
	}
}

func (cm *ChairManager) CompleteRide(chairID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if state, ok := cm.chairs[chairID]; ok {
		state.CurrentRideID = ""
	}
}

func (cm *ChairManager) UnassignRide(chairID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if state, ok := cm.chairs[chairID]; ok {
		state.CurrentRideID = ""
	}
}

// FindBestAvailableChair は利用可能な椅子の中で到着時間が最も短い椅子を選択し、即座に rideID を割り当てます
func (cm *ChairManager) FindBestAvailableChair(pickupLat, pickupLon int, rideID string) (*ChairState, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var bestChair *ChairState
	bestTime := math.MaxFloat64
	bestDistance := math.MaxInt

	for _, state := range cm.chairs {
		if !state.IsActive || !state.HasLocation || state.CurrentRideID != "" {
			continue
		}

		dist := calculateDistance(pickupLat, pickupLon, state.Latitude, state.Longitude)
		// 到着予測時間 = 距離 / 速度
		estimatedTime := float64(dist) / float64(state.Speed)

		if estimatedTime < bestTime || (estimatedTime == bestTime && dist < bestDistance) {
			bestTime = estimatedTime
			bestDistance = dist
			bestChair = state
		}
	}

	if bestChair == nil {
		return nil, false
	}

	bestChair.CurrentRideID = rideID
	// コピーを返す
	copyState := *bestChair
	return &copyState, true
}

func (cm *ChairManager) GetNearbyChairs(lat, lon, distance int) []appGetNearbyChairsResponseChair {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	nearby := make([]appGetNearbyChairsResponseChair, 0)
	for _, state := range cm.chairs {
		if !state.IsActive || !state.HasLocation || state.CurrentRideID != "" {
			continue
		}

		if calculateDistance(lat, lon, state.Latitude, state.Longitude) <= distance {
			nearby = append(nearby, appGetNearbyChairsResponseChair{
				ID:    state.ID,
				Name:  state.Name,
				Model: state.Model,
				CurrentCoordinate: Coordinate{
					Latitude:  state.Latitude,
					Longitude: state.Longitude,
				},
			})
		}
	}
	return nearby
}
