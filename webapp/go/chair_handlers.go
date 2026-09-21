package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/isucon/isucon14/webapp/go/cache"
	"github.com/isucon/isucon14/webapp/go/models"
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

	owner, err := ownerRepository.GetByChairRegisterToken(ctx, req.ChairRegisterToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid chair_register_token"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	chairID := ulid.Make().String()
	accessToken := secureRandomStr(32)

	if err := chairRepository.Create(ctx, db, &Chair{
		ID:          chairID,
		OwnerID:     owner.ID,
		Name:        req.Name,
		Model:       req.Model,
		IsActive:    false,
		AccessToken: accessToken,
	}); err != nil {
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

	if err := chairRepository.UpdateIsActive(ctx, db, chair.ID, req.IsActive); err != nil {
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

// 同一椅子の座標POST直列化用ストライプドロック。
// 直前位置の読取(GetLocation)→距離加算→最新位置の公開(UpdateLocation)を
// 椅子毎に直列化し、同時POSTによる二重計上・逆転を防ぐ。
// (benchは椅子毎に逐次POSTだが、クライアントタイムアウト後のリトライ重複や
//  サーバ側の遅延で重なる場合がある。単一appプロセス前提)
// ロック保持がTX時間（数十ms）に及ぶため、異椅子間の衝突を避ける目的で
// 十分に粗く取る（65536本で1500台なら共有はほぼ発生しない）。
var coordinateLocks [65536]sync.Mutex

func coordinateLockFor(chairID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(chairID))
	return &coordinateLocks[h.Sum32()%uint32(len(coordinateLocks))]
}

func chairPostCoordinate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &Coordinate{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	chair := ctx.Value("chair").(*Chair)

	lk := coordinateLockFor(chair.ID)
	lk.Lock()
	defer lk.Unlock()

	// 直前の位置情報の有無は診断用に数える（距離の差分計算は DistanceBuffer が
	// POST毎の基準点を自前保持するため、ChairManagerの状態に依存しない）。
	_, _, hasPrev := globalChairManager.GetLocation(chair.ID)

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

	// 走行距離の追跡は DistanceBuffer がPOST毎に行う。
	// ChairManagerのエントリ有無に依存しないため、欠落時も追跡継続する。
	// DB往復（Begin/upsert/Commit）をホットパスから外すのが目的で、
	// TX内原子性は不要: 遷移判定の読取は不変キャッシュ＋最新コミット読みで等価、
	// PICKUP/ARRIVED行は下で単発INSERTする。
	noteCoordPost(hasPrev)
	globalDistanceBuffer.Add(chair.ID, req.Latitude, req.Longitude, recordedAt)
	// ticker flush の停滞保険。正常時は atomic load＋分岐のみ。
	globalDistanceBuffer.MaybeRepair(ctx, recordedAt)

	rideID, hasRide := globalChairManager.GetCurrentRideID(chair.ID)
	statusChanged := false
	insertedStatus := ""
	insertedStatusID := ""
	var changedCoords cache.RideCoords
	if !hasRide {
		// 未割当時は GetLatestByChairID が ErrNoRows の場合と同等で遷移判定不要
	} else {
		coords, err := globalRideCoordsCache.Get(ctx, rideID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		status, err := globalStatusCache.Get(ctx, db, rideID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "COMPLETED" && status != "CANCELED" {
			if req.Latitude == coords.PickupLatitude && req.Longitude == coords.PickupLongitude && status == "ENROUTE" {
				insertedStatusID = ulid.Make().String()
				// 遷移行のINSERTは後追いバッチ化する（受諾・出発と同等の理由）。
				globalRideStatusBuffer.Append(models.RideStatus{
					ID:        insertedStatusID,
					RideID:    rideID,
					Status:    "PICKUP",
					CreatedAt: time.Now(),
				})
				statusChanged = true
				insertedStatus = "PICKUP"
			}

			if req.Latitude == coords.DestinationLatitude && req.Longitude == coords.DestinationLongitude && status == "CARRYING" {
				insertedStatusID = ulid.Make().String()
				globalRideStatusBuffer.Append(models.RideStatus{
					ID:        insertedStatusID,
					RideID:    rideID,
					Status:    "ARRIVED",
					CreatedAt: time.Now(),
				})
				statusChanged = true
				insertedStatus = "ARRIVED"
			}
		}
		changedCoords = coords
	}

	// PICKUP/ARRIVED 遷移時のみ両SSEを起床させ、キャッシュを更新する
	// （移動のみの座標更新では起床しない）
	if statusChanged {
		globalStatusCache.Set(changedCoords.RideID, insertedStatus)
		// 未送信ログへの登録は wake より先に行い、起床後の取得漏れを防ぐ
		globalStatusLog.Append(insertedStatusID, changedCoords.RideID, insertedStatus, changedCoords.UserID, chair.ID, time.Now())
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
			// 未送信はインメモリの StatusLog から取得する（DBポーリング排除）。
			// 全遷移が commit 後・wake 前に Append されるため等価。
			return globalStatusLog.ListUnsentForChair(chair.ID, sseUnsentBatchSize), nil
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
			return rideRepository.GetByIDCached(ctx, rideID)
		},
		func(ctx context.Context, ride *Ride, status string) (any, error) {
			return buildChairNotificationData(ctx, ride, status)
		},
		func(ctx context.Context, id string) error {
			// メモリ追跡の解除とDBの送信済みUPDATEを併用する。
			// DB側は SentMarkBuffer に束ねて後追い flush する。
			globalStatusLog.MarkChairSent(id)
			globalSentMarkBuffer.MarkChair(id)
			return nil
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

	// 遷移行のINSERTをバッファ化したためTXは不要。読取はautocommitで
	// 行い、前提条件の判定は最新コミット読みで行う（従来より新鮮）。
	ride, err := rideRepository.GetAssignmentByID(ctx, db, rideID)
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

	// 未送信ログ用の状態ID。Append の後の wake 前に登録するため保持する
	statusID := ""
	switch req.Status {
	// Acknowledge the ride
	case "ENROUTE":
		statusID = ulid.Make().String()
		globalRideStatusBuffer.Append(models.RideStatus{
			ID:        statusID,
			RideID:    ride.ID,
			Status:    "ENROUTE",
			CreatedAt: time.Now(),
		})
	// After Picking up user
	case "CARRYING":
		status, err := globalStatusCache.Get(ctx, db, ride.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "PICKUP" {
			writeError(w, http.StatusBadRequest, errors.New("chair has not arrived yet"))
			return
		}
		statusID = ulid.Make().String()
		globalRideStatusBuffer.Append(models.RideStatus{
			ID:        statusID,
			RideID:    ride.ID,
			Status:    "CARRYING",
			CreatedAt: time.Now(),
		})
	default:
		writeError(w, http.StatusBadRequest, errors.New("invalid status"))
		return
	}

	// ENROUTE/CARRYING 遷移を両SSEに通知し、キャッシュを更新する
	// 未送信ログへの登録は wake より先に行い、起床後の取得漏れを防ぐ
	globalStatusLog.Append(statusID, ride.ID, req.Status, ride.UserID, chair.ID, time.Now())
	WakeChair(chair.ID)
	WakeUser(ride.UserID)
	globalStatusCache.Set(ride.ID, req.Status)

	w.WriteHeader(http.StatusNoContent)
}
