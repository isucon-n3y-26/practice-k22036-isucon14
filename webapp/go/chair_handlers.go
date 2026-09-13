package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/isucon/isucon14/webapp/go/cache"
)

type chairPostChairsRequest struct {
	Name               string `json:"name"`
	Model              string `json:"model"`
	ChairRegisterToken string `json:"chair_register_token"`
}

type chairPostChairsResponse struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
}

func chairPostChairs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &chairPostChairsRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Model == "" || req.ChairRegisterToken == "" {
		writeError(w, http.StatusBadRequest, errors.New("some of required fields(name, model, chair_register_token) are empty"))
		return
	}

	owner := &Owner{}
	if err := db.GetContext(ctx, owner, "SELECT * FROM owners WHERE chair_register_token = ?", req.ChairRegisterToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid chair_register_token"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	chairID := ulid.Make().String()
	accessToken := secureRandomStr(32)

	if _, err := db.ExecContext(
		ctx,
		"INSERT INTO chairs (id, owner_id, name, model, is_active, access_token) VALUES (?, ?, ?, ?, ?, ?)",
		chairID, owner.ID, req.Name, req.Model, false, accessToken,
	); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	globalChairManager.RegisterChair(chairID, req.Name, req.Model)

	http.SetCookie(w, &http.Cookie{
		Path:  "/",
		Name:  "chair_session",
		Value: accessToken,
	})

	writeJSON(w, http.StatusCreated, &chairPostChairsResponse{
		ID:      chairID,
		OwnerID: owner.ID,
	})
}

type postChairActivityRequest struct {
	IsActive bool `json:"is_active"`
}

func chairPostActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	chair := ctx.Value("chair").(*Chair)

	req := &postChairActivityRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_, err := db.ExecContext(ctx, "UPDATE chairs SET is_active = ? WHERE id = ?", req.IsActive, chair.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	globalChairManager.SetActivity(chair.ID, req.IsActive)

	if req.IsActive {
		triggerMatching()
	}

	w.WriteHeader(http.StatusNoContent)
}

type chairPostCoordinateResponse struct {
	RecordedAt int64 `json:"recorded_at"`
}

func chairPostCoordinate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &Coordinate{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	chair := ctx.Value("chair").(*Chair)

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	// 直前の位置情報はインメモリの椅子管理から取得する（走行距離の差分計算用）。
	// ChairManager.GetLocation は直前位置SELECTと等価のため、SELECT 1本を削減できる。
	prevLat, prevLon, hasPrev := globalChairManager.GetLocation(chair.ID)

	chairLocationID := ulid.Make().String()
	// 位置行のINSERTは後追いバッチ化する。実行時の読手は存在せず
	// （最新位置は ChairManager、応答時刻は下の recordedAt）、
	// created_at はこの瞬間の時刻を明示挿入して等価に保つ。
	recordedAt := time.Now()
	globalLocationBuffer.Append(locationEntry{
		ID:        chairLocationID,
		ChairID:   chair.ID,
		Latitude:  req.Latitude,
		Longitude: req.Longitude,
		CreatedAt: recordedAt,
	})

	// 走行距離を累積
	if hasPrev {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO chair_total_distances (chair_id, total_distance) VALUES (?, ?)
			 ON DUPLICATE KEY UPDATE total_distance = total_distance + VALUES(total_distance)`,
			chair.ID,
			calculateDistance(prevLat, prevLon, req.Latitude, req.Longitude),
		); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	rideID, hasRide := globalChairManager.GetCurrentRideID(chair.ID)
	statusChanged := false
	insertedStatus := ""
	var changedCoords cache.RideCoords
	if !hasRide {
		// 未割当時は GetLatestByChairID が ErrNoRows の場合と同等で遷移判定不要
	} else {
		coords, err := globalRideCoordsCache.Get(ctx, rideID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		status, err := globalStatusCache.Get(ctx, tx, rideID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "COMPLETED" && status != "CANCELED" {
			if req.Latitude == coords.PickupLatitude && req.Longitude == coords.PickupLongitude && status == "ENROUTE" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), rideID, "PICKUP"); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				statusChanged = true
				insertedStatus = "PICKUP"
			}

			if req.Latitude == coords.DestinationLatitude && req.Longitude == coords.DestinationLongitude && status == "CARRYING" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), rideID, "ARRIVED"); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				statusChanged = true
				insertedStatus = "ARRIVED"
			}
		}
		changedCoords = coords
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// PICKUP/ARRIVED 遷移時のみ両SSEを起床させ、キャッシュを更新する
	// （移動のみの座標更新では起床しない）
	if statusChanged {
		globalStatusCache.Set(changedCoords.RideID, insertedStatus)
		WakeChair(chair.ID)
		WakeUser(changedCoords.UserID)
	}

	globalChairManager.UpdateLocation(chair.ID, req.Latitude, req.Longitude)

	writeJSON(w, http.StatusOK, &chairPostCoordinateResponse{
		RecordedAt: recordedAt.UnixMilli(),
	})
}

type simpleUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type chairGetNotificationResponse struct {
	Data         *chairGetNotificationResponseData `json:"data"`
	RetryAfterMs int                               `json:"retry_after_ms"`
}

type chairGetNotificationResponseData struct {
	RideID                string     `json:"ride_id"`
	User                  simpleUser `json:"user"`
	PickupCoordinate      Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate `json:"destination_coordinate"`
	Status                string     `json:"status"`
}

// GET /api/chair/notification (SSE)
func chairGetNotification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	chair := ctx.Value("chair").(*Chair)

	stream, ok := NewSSEStream(w, r)
	if !ok {
		return
	}
	wake, unsub := subscribeChair(chair.ID)
	defer unsub()

	StreamRideNotifications(
		stream,
		func(ctx context.Context) ([]RideStatus, error) {
			return rideStatusRepository.ListUnsentChairByChairID(ctx, db, chair.ID, sseUnsentBatchSize)
		},
		func(ctx context.Context) (*Ride, string, error) {
			// 接続直後は即座に最新のライド状態を送信する
			ride, err := rideRepository.GetLatestByChairID(ctx, db, chair.ID)
			if err != nil {
				return nil, "", err
			}
			status, err := globalStatusCache.Get(ctx, db, ride.ID)
			if err != nil {
				return nil, "", err
			}
			return ride, status, nil
		},
		func(ctx context.Context, rideID string) (*Ride, error) {
			return rideRepository.GetByID(ctx, db, rideID)
		},
		func(ctx context.Context, ride *Ride, status string) (any, error) {
			return buildChairNotificationData(ctx, ride, status)
		},
		func(ctx context.Context, id string) error {
			return rideStatusRepository.MarkChairSent(ctx, db, id)
		},
		wake,
	)
}

func buildChairNotificationData(ctx context.Context, ride *Ride, status string) (*chairGetNotificationResponseData, error) {
	user, err := userRepository.GetByID(ctx, ride.UserID)
	if err != nil {
		return nil, err
	}

	return &chairGetNotificationResponseData{
		RideID: ride.ID,
		User: simpleUser{
			ID:   user.ID,
			Name: fmt.Sprintf("%s %s", user.Firstname, user.Lastname),
		},
		PickupCoordinate: Coordinate{
			Latitude:  ride.PickupLatitude,
			Longitude: ride.PickupLongitude,
		},
		DestinationCoordinate: Coordinate{
			Latitude:  ride.DestinationLatitude,
			Longitude: ride.DestinationLongitude,
		},
		Status: status,
	}, nil
}

type postChairRidesRideIDStatusRequest struct {
	Status string `json:"status"`
}

func chairPostRideStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rideID := r.PathValue("ride_id")

	chair := ctx.Value("chair").(*Chair)

	req := &postChairRidesRideIDStatusRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	ride, err := rideRepository.GetAssignmentByID(ctx, tx, rideID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("ride not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if ride.ChairID.String != chair.ID {
		writeError(w, http.StatusBadRequest, errors.New("not assigned to this ride"))
		return
	}

	switch req.Status {
	// Acknowledge the ride
	case "ENROUTE":
		if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "ENROUTE"); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	// After Picking up user
	case "CARRYING":
		status, err := globalStatusCache.Get(ctx, tx, ride.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "PICKUP" {
			writeError(w, http.StatusBadRequest, errors.New("chair has not arrived yet"))
			return
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "CARRYING"); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	default:
		writeError(w, http.StatusBadRequest, errors.New("invalid status"))
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// ENROUTE/CARRYING 遷移を両SSEに通知し、キャッシュを更新する
	WakeChair(chair.ID)
	WakeUser(ride.UserID)
	globalStatusCache.Set(ride.ID, req.Status)

	w.WriteHeader(http.StatusNoContent)
}
