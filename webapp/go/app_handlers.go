package main

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// queryGetter は *sqlx.DB と *sqlx.Tx の両方を受け付けるためのインターフェース
type queryGetter interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

type appPostUsersRequest struct {
	Username       string  `json:"username"`
	FirstName      string  `json:"firstname"`
	LastName       string  `json:"lastname"`
	DateOfBirth    string  `json:"date_of_birth"`
	InvitationCode *string `json:"invitation_code"`
}

type appPostUsersResponse struct {
	ID             string `json:"id"`
	InvitationCode string `json:"invitation_code"`
}

func appPostUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &appPostUsersRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" || req.FirstName == "" || req.LastName == "" || req.DateOfBirth == "" {
		writeError(w, http.StatusBadRequest, errors.New("required fields(username, firstname, lastname, date_of_birth) are empty"))
		return
	}

	// deadlock (1213) / lock wait timeout (1205) は backoff 付きで再試行する
	var userID, accessToken, invitationCode string
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		userID, accessToken, invitationCode, err = insertUserTx(ctx, req)
		if err == nil {
			break
		}
		if errors.Is(err, errInvitationInvalid) {
			break
		}
		if !isRetryableDBError(err) || attempt == 4 {
			break
		}
		retryBackoff(attempt + 1)
	}
	if err != nil {
		if errors.Is(err, errInvitationInvalid) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Path:  "/",
		Name:  "app_session",
		Value: accessToken,
	})

	writeJSON(w, http.StatusCreated, &appPostUsersResponse{
		ID:             userID,
		InvitationCode: invitationCode,
	})
}

var errInvitationInvalid = errors.New("この招待コードは使用できません。")

// 招待コード単位の直列化用ストライプドロック。
// 同一コードへのburst登録による coupons デッドロックをアプリ側で抑止する。
// (単一appプロセス前提。invite上限3の判定とINSERTを同一コード毎に直列化する)
var inviteLocks [256]sync.Mutex

func inviteLockFor(code string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(code))
	return &inviteLocks[h.Sum32()%uint32(len(inviteLocks))]
}

// retryBackoff は deadlock 再試行前の待機。再試行同士の再衝突を避けるため逓増させる。
func retryBackoff(attempt int) {
	time.Sleep(time.Duration(attempt*attempt*5+attempt) * time.Millisecond)
}

// insertUserTx はユーザー登録〜クーポン付与〜COMMITまでを行う。
// deadlock等の再試行は呼出側(appPostUsers)が行う。
func insertUserTx(ctx context.Context, req *appPostUsersRequest) (userID, accessToken, invitationCode string, err error) {
	userID = ulid.Make().String()
	accessToken = secureRandomStr(32)
	invitationCode = secureRandomStr(15)

	// 招待コード付き登録は同一コード毎に直列化し、coupons の burst deadlock を抑止する
	if req.InvitationCode != nil && *req.InvitationCode != "" {
		lk := inviteLockFor("INV_" + *req.InvitationCode)
		lk.Lock()
		defer lk.Unlock()
	}

	tx, err := db.Beginx()
	if err != nil {
		return "", "", "", err
	}
	defer tx.Rollback()

	if err := userRepository.Create(ctx, tx, &User{
		ID:             userID,
		Username:       req.Username,
		Firstname:      req.FirstName,
		Lastname:       req.LastName,
		DateOfBirth:    req.DateOfBirth,
		AccessToken:    accessToken,
		InvitationCode: invitationCode,
	}); err != nil {
		return "", "", "", err
	}

	// 初回登録キャンペーンのクーポンを付与
	if err := couponRepository.Create(ctx, tx, &Coupon{
		UserID:   userID,
		Code:     "CP_NEW2024",
		Discount: 3000,
	}); err != nil {
		return "", "", "", err
	}

	// 招待コードを使った登録
	if req.InvitationCode != nil && *req.InvitationCode != "" {
		// 招待する側の招待数をチェック
		inviteCount, err := couponRepository.CountByCodeForUpdate(ctx, tx, "INV_"+*req.InvitationCode)
		if err != nil {
			return "", "", "", err
		}
		if inviteCount >= 3 {
			return "", "", "", errInvitationInvalid
		}

		// ユーザーチェック
		inviter, err := userRepository.GetByInvitationCode(ctx, *req.InvitationCode)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", "", errInvitationInvalid
			}
			return "", "", "", err
		}

		// 招待クーポン付与と招待した人へのReward付与を1文にまとめる
		if err := couponRepository.CreateInvitationPair(ctx, tx, userID, *req.InvitationCode, inviter.ID); err != nil {
			return "", "", "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", "", "", err
	}

	return userID, accessToken, invitationCode, nil
}

type appPostPaymentMethodsRequest struct {
	Token string `json:"token"`
}

func appPostPaymentMethods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &appPostPaymentMethodsRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, errors.New("token is required but was empty"))
		return
	}

	user := ctx.Value("user").(*User)

	if err := paymentTokenRepository.Create(ctx, db, user.ID, req.Token); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type getAppRidesResponse struct {
	Rides []getAppRidesResponseItem `json:"rides"`
}

type getAppRidesResponseItem struct {
	ID                    string                       `json:"id"`
	PickupCoordinate      Coordinate                   `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate                   `json:"destination_coordinate"`
	Chair                 getAppRidesResponseItemChair `json:"chair"`
	Fare                  int                          `json:"fare"`
	Evaluation            int                          `json:"evaluation"`
	RequestedAt           int64                        `json:"requested_at"`
	CompletedAt           int64                        `json:"completed_at"`
}

type getAppRidesResponseItemChair struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

// 完了済みライド履歴を返す。ライド毎の関連取得は3回の一括取得にまとめる。
func appGetRides(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := ctx.Value("user").(*User)

	// 履歴対象の完了ライド一覧
	rides, err := rideRepository.ListCompletedByUserID(ctx, db, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 運賃・椅子・オーナー表示用の関連データを一括取得してmap化する
	discountByRideID := make(map[string]int, len(rides))
	chairByID := map[string]Chair{}
	ownerNameByID := map[string]string{}
	if len(rides) > 0 {
		rideIDs := make([]string, 0, len(rides))
		chairIDSet := make(map[string]struct{}, len(rides))
		for _, ride := range rides {
			rideIDs = append(rideIDs, ride.ID)
			chairIDSet[ride.ChairID.String] = struct{}{}
		}

		// ライドに紐づくクーポンの割引額
		coupons, err := couponRepository.ListByUsedByIDs(ctx, db, rideIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, coupon := range coupons {
			if coupon.UsedBy != nil {
				discountByRideID[*coupon.UsedBy] = coupon.Discount
			}
		}

		// 配車された椅子（重複排除）
		chairIDs := make([]string, 0, len(chairIDSet))
		for id := range chairIDSet {
			chairIDs = append(chairIDs, id)
		}
		chairs, err := chairRepository.ListByIDs(ctx, db, chairIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		ownerIDSet := make(map[string]struct{}, len(chairs))
		for _, chair := range chairs {
			chairByID[chair.ID] = chair
			ownerIDSet[chair.OwnerID] = struct{}{}
		}

		// 椅子の所属オーナー名（重複排除）
		ownerIDs := make([]string, 0, len(ownerIDSet))
		for id := range ownerIDSet {
			ownerIDs = append(ownerIDs, id)
		}
		owners, err := ownerRepository.ListByIDs(ctx, db, ownerIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, owner := range owners {
			ownerNameByID[owner.ID] = owner.Name
		}
	}

	// 履歴1件ごとに運賃と椅子情報を組み立てる
	items := []getAppRidesResponseItem{}
	for _, ride := range rides {
		// クーポン未使用時はゼロ値(0)で、旧calculateDiscountedFareのミス時と等価
		fare := calculateFareWithDiscount(ride.PickupLatitude, ride.PickupLongitude, ride.DestinationLatitude, ride.DestinationLongitude, discountByRideID[ride.ID])

		chair := chairByID[ride.ChairID.String]

		items = append(items, getAppRidesResponseItem{
			ID:                    ride.ID,
			PickupCoordinate:      Coordinate{Latitude: ride.PickupLatitude, Longitude: ride.PickupLongitude},
			DestinationCoordinate: Coordinate{Latitude: ride.DestinationLatitude, Longitude: ride.DestinationLongitude},
			Fare:                  fare,
			Evaluation:            *ride.Evaluation,
			RequestedAt:           ride.CreatedAt.UnixMilli(),
			CompletedAt:           ride.UpdatedAt.UnixMilli(),
			Chair: getAppRidesResponseItemChair{
				ID:    chair.ID,
				Name:  chair.Name,
				Model: chair.Model,
				Owner: ownerNameByID[chair.OwnerID],
			},
		})
	}

	writeJSON(w, http.StatusOK, &getAppRidesResponse{
		Rides: items,
	})
}

type appPostRidesRequest struct {
	PickupCoordinate      *Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate *Coordinate `json:"destination_coordinate"`
}

type appPostRidesResponse struct {
	RideID string `json:"ride_id"`
	Fare   int    `json:"fare"`
}

func appPostRides(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &appPostRidesRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.PickupCoordinate == nil || req.DestinationCoordinate == nil {
		writeError(w, http.StatusBadRequest, errors.New("required fields(pickup_coordinate, destination_coordinate) are empty"))
		return
	}

	user := ctx.Value("user").(*User)

	// deadlock (1213) / lock wait timeout (1205) は backoff 付きで再試行する
	var rideID, matchingStatusID string
	var fare int
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		rideID, matchingStatusID, fare, err = insertRideTx(ctx, user, req)
		if err == nil {
			break
		}
		if errors.Is(err, errRideExists) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if !isRetryableDBError(err) || attempt == 4 {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		retryBackoff(attempt + 1)
	}

	// MATCHING 作成をユーザー向けSSEに通知する（接続済みフロント用）
	// 未送信ログへの登録は wake より先に行い、起床後の取得漏れを防ぐ
	globalStatusLog.Append(matchingStatusID, rideID, "MATCHING", user.ID, "", time.Now())
	WakeUser(user.ID)
	globalStatusCache.Set(rideID, "MATCHING")

	triggerMatching()

	writeJSON(w, http.StatusAccepted, &appPostRidesResponse{
		RideID: rideID,
		Fare:   fare,
	})
}

var errRideExists = errors.New("ride already exists")

// insertRideTx は配車INSERT〜coupon確定〜COMMITまでを行う。
// deadlock等の再試行は呼出側(appPostRides)が行う。
func insertRideTx(ctx context.Context, user *User, req *appPostRidesRequest) (rideID, matchingStatusID string, fare int, err error) {
	rideID = ulid.Make().String()

	tx, err := db.Beginx()
	if err != nil {
		return "", "", 0, err
	}
	defer tx.Rollback()

	continuingRideCount, err := rideRepository.CountContinuingByUserID(ctx, tx, user.ID)
	if err != nil {
		return "", "", 0, err
	}

	if continuingRideCount > 0 {
		return "", "", 0, errRideExists
	}

	if err := rideRepository.Create(ctx, tx, rideID, user.ID, req.PickupCoordinate.Latitude, req.PickupCoordinate.Longitude, req.DestinationCoordinate.Latitude, req.DestinationCoordinate.Longitude); err != nil {
		return "", "", 0, err
	}

	matchingStatusID = ulid.Make().String()
	if err := rideStatusRepository.Create(ctx, tx, matchingStatusID, rideID, "MATCHING"); err != nil {
		return "", "", 0, err
	}

	// マッチング待ちキューに登録する（同一トランザクション）
	if err := matchingQueueRepository.Enqueue(ctx, tx, rideID); err != nil {
		return "", "", 0, err
	}

	rideCount, err := rideRepository.CountByUserID(ctx, tx, user.ID)
	if err != nil {
		return "", "", 0, err
	}

	var coupon Coupon
	if rideCount == 1 {
		// 初回利用で、初回利用クーポンがあれば必ず使う
		newUserCoupon, err := couponRepository.GetNewUserCouponForUpdate(ctx, tx, user.ID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return "", "", 0, err
			}

			// 無ければ他のクーポンを付与された順番に使う
			oldest, err := couponRepository.GetOldestUnusedForUpdate(ctx, tx, user.ID)
			if err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					return "", "", 0, err
				}
			} else {
				coupon = *oldest
				if err := couponRepository.ClaimByCode(ctx, tx, rideID, user.ID, coupon.Code); err != nil {
					return "", "", 0, err
				}
			}
		} else {
			coupon = *newUserCoupon
			if err := couponRepository.ClaimByCode(ctx, tx, rideID, user.ID, coupon.Code); err != nil {
				return "", "", 0, err
			}
		}
	} else {
		// 他のクーポンを付与された順番に使う
		oldest, err := couponRepository.GetOldestUnusedForUpdate(ctx, tx, user.ID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return "", "", 0, err
			}
		} else {
			coupon = *oldest
			if err := couponRepository.ClaimByCode(ctx, tx, rideID, user.ID, coupon.Code); err != nil {
				return "", "", 0, err
			}
		}
	}

	// 直上で確定させた coupon が手元にあるため再取得せず割引額を直接使う。
	// 未取得の場合 coupon.Discount はゼロ値(0)で、再取得ミス時と等価。
	fare = calculateFareWithDiscount(req.PickupCoordinate.Latitude, req.PickupCoordinate.Longitude, req.DestinationCoordinate.Latitude, req.DestinationCoordinate.Longitude, coupon.Discount)

	if err := tx.Commit(); err != nil {
		return "", "", 0, err
	}

	return rideID, matchingStatusID, fare, nil
}

type appPostRidesEstimatedFareRequest struct {
	PickupCoordinate      *Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate *Coordinate `json:"destination_coordinate"`
}

type appPostRidesEstimatedFareResponse struct {
	Fare     int `json:"fare"`
	Discount int `json:"discount"`
}

func appPostRidesEstimatedFare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &appPostRidesEstimatedFareRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.PickupCoordinate == nil || req.DestinationCoordinate == nil {
		writeError(w, http.StatusBadRequest, errors.New("required fields(pickup_coordinate, destination_coordinate) are empty"))
		return
	}

	user := ctx.Value("user").(*User)

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	discounted, err := calculateDiscountedFare(ctx, tx, user.ID, nil, req.PickupCoordinate.Latitude, req.PickupCoordinate.Longitude, req.DestinationCoordinate.Latitude, req.DestinationCoordinate.Longitude)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, &appPostRidesEstimatedFareResponse{
		Fare:     discounted,
		Discount: calculateFare(req.PickupCoordinate.Latitude, req.PickupCoordinate.Longitude, req.DestinationCoordinate.Latitude, req.DestinationCoordinate.Longitude) - discounted,
	})
}

// マンハッタン距離を求める
func calculateDistance(aLatitude, aLongitude, bLatitude, bLongitude int) int {
	return abs(aLatitude-bLatitude) + abs(aLongitude-bLongitude)
}
func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

type appPostRideEvaluationRequest struct {
	Evaluation int `json:"evaluation"`
}

type appPostRideEvaluationResponse struct {
	CompletedAt int64 `json:"completed_at"`
}

func appPostRideEvaluatation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rideID := r.PathValue("ride_id")

	req := &appPostRideEvaluationRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Evaluation < 1 || req.Evaluation > 5 {
		writeError(w, http.StatusBadRequest, errors.New("evaluation must be between 1 and 5"))
		return
	}

	// 決済は外部HTTP（リトライで最大数秒）であり、開いたTXを保持したまま
	// 呼ぶとロック・コネクションを圧迫するため、書込みTXの前に済ませる。
	// 決済失敗時は従来どおり何も書込まず500/502を返すため、意味は不変。
	// リトライ判定のライド一覧は書込み前後で不変のため、TXの代わりにdbで読む。
	ride, err := rideRepository.GetByID(ctx, db, rideID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("ride not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status, err := globalStatusCache.Get(ctx, db, ride.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if status != "ARRIVED" {
		writeError(w, http.StatusBadRequest, errors.New("not arrived yet"))
		return
	}

	paymentToken, err := paymentTokenRepository.GetByUserID(ctx, db, ride.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, errors.New("payment token not registered"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	fare, err := calculateDiscountedFare(ctx, db, ride.UserID, ride, ride.PickupLatitude, ride.PickupLongitude, ride.DestinationLatitude, ride.DestinationLongitude)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paymentGatewayRequest := &paymentGatewayPostPaymentRequest{
		Amount: fare,
	}

	var paymentGatewayURL = paymentGatewayBaseURL

	if err := requestPaymentGatewayPostPayment(ctx, paymentGatewayURL, paymentToken.Token, paymentGatewayRequest, func() ([]Ride, error) {
		return rideRepository.ListByUserID(ctx, db, ride.UserID)
	}); err != nil {
		if errors.Is(err, erroredUpstream) {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	count, err := rideRepository.UpdateEvaluation(ctx, tx, rideID, req.Evaluation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, errors.New("ride not found"))
		return
	}

	completedStatusID := ulid.Make().String()
	if err := rideStatusRepository.Create(ctx, tx, completedStatusID, rideID, "COMPLETED"); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 応答の completed_at は履歴表示と同一時計（DB時刻）にするため再取得する
	ride, err = rideRepository.GetByID(ctx, tx, rideID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("ride not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if ride.ChairID.Valid {
		globalChairManager.CompleteRide(ride.ChairID.String)
	}

	// COMPLETED 作成を両SSEに通知する
	// 未送信ログへの登録は wake より先に行い、起床後の取得漏れを防ぐ
	chairID := ""
	if ride.ChairID.Valid {
		chairID = ride.ChairID.String
	}
	globalStatusLog.Append(completedStatusID, rideID, "COMPLETED", ride.UserID, chairID, time.Now())
	WakeUser(ride.UserID)
	if ride.ChairID.Valid {
		WakeChair(ride.ChairID.String)
	}
	globalStatusCache.Set(rideID, "COMPLETED")

	triggerMatching()

	writeJSON(w, http.StatusOK, &appPostRideEvaluationResponse{
		CompletedAt: ride.UpdatedAt.UnixMilli(),
	})
}

type appGetNotificationResponse struct {
	Data         *appGetNotificationResponseData `json:"data"`
	RetryAfterMs int                             `json:"retry_after_ms"`
}

type appGetNotificationResponseData struct {
	RideID                string                           `json:"ride_id"`
	PickupCoordinate      Coordinate                       `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate                       `json:"destination_coordinate"`
	Fare                  int                              `json:"fare"`
	Status                string                           `json:"status"`
	Chair                 *appGetNotificationResponseChair `json:"chair,omitempty"`
	CreatedAt             int64                            `json:"created_at"`
	UpdateAt              int64                            `json:"updated_at"`
}

type appGetNotificationResponseChair struct {
	ID    string                               `json:"id"`
	Name  string                               `json:"name"`
	Model string                               `json:"model"`
	Stats appGetNotificationResponseChairStats `json:"stats"`
}

type appGetNotificationResponseChairStats struct {
	TotalRidesCount    int     `json:"total_rides_count"`
	TotalEvaluationAvg float64 `json:"total_evaluation_avg"`
}

// GET /api/app/notification (SSE)
func appGetNotification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := ctx.Value("user").(*User)

	stream, ok := NewSSEStream(w, r)
	if !ok {
		return
	}
	wake, unsub := subscribeUser(user.ID)
	defer unsub()

	StreamRideNotifications(
		stream,
		func(ctx context.Context) ([]RideStatus, error) {
			// 未送信はインメモリの StatusLog から取得する（DBポーリング排除）。
			// 全遷移が commit 後・wake 前に Append されるため等価。
			return globalStatusLog.ListUnsentForUser(user.ID, sseUnsentBatchSize), nil
		},
		func(ctx context.Context) (*Ride, string, error) {
			// 接続直後は即座に最新のライド状態を送信する
			ride, err := rideRepository.GetLatestByUserID(ctx, db, user.ID)
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
			return buildAppNotificationData(ctx, db, user.ID, ride, status)
		},
		func(ctx context.Context, id string) error {
			// メモリ追跡の解除とDBの送信済みUPDATEを併用する
			globalStatusLog.MarkAppSent(id)
			return rideStatusRepository.MarkAppSent(ctx, db, id)
		},
		wake,
	)
}

func buildAppNotificationData(ctx context.Context, q queryGetter, userID string, ride *Ride, status string) (*appGetNotificationResponseData, error) {
	fare, err := calculateDiscountedFare(ctx, q, userID, ride, ride.PickupLatitude, ride.PickupLongitude, ride.DestinationLatitude, ride.DestinationLongitude)
	if err != nil {
		return nil, err
	}

	data := &appGetNotificationResponseData{
		RideID: ride.ID,
		PickupCoordinate: Coordinate{
			Latitude:  ride.PickupLatitude,
			Longitude: ride.PickupLongitude,
		},
		DestinationCoordinate: Coordinate{
			Latitude:  ride.DestinationLatitude,
			Longitude: ride.DestinationLongitude,
		},
		Fare:      fare,
		Status:    status,
		CreatedAt: ride.CreatedAt.UnixMilli(),
		UpdateAt:  ride.UpdatedAt.UnixMilli(),
	}

	if ride.ChairID.Valid {
		chair, err := chairRepository.GetByID(ctx, q, ride.ChairID.String)
		if err != nil {
			return nil, err
		}
		stats, err := getChairStats(ctx, q, chair.ID)
		if err != nil {
			return nil, err
		}
		data.Chair = &appGetNotificationResponseChair{
			ID:    chair.ID,
			Name:  chair.Name,
			Model: chair.Model,
			Stats: stats,
		}
	}

	return data, nil
}

// getChairStats はユーザー向け通知に含める椅子の統計情報
// （完了ライド数と評価平均）を返す。
// 完了の定義は ARRIVED・CARRYING・COMPLETED の各ステータスを
// すべて含むライドであること。集計自体は
// ChairRepository.GetCompletedStats に1クエリで委譲しており、
// 戻り値を通知用の型に詰め替える薄いラッパーである。
func getChairStats(ctx context.Context, q queryGetter, chairID string) (appGetNotificationResponseChairStats, error) {
	stats := appGetNotificationResponseChairStats{}

	s, err := chairRepository.GetCompletedStats(ctx, q, chairID)
	if err != nil {
		return stats, err
	}

	stats.TotalRidesCount = s.TotalRidesCount
	stats.TotalEvaluationAvg = s.TotalEvaluationAvg

	return stats, nil
}

type appGetNearbyChairsResponse struct {
	Chairs      []appGetNearbyChairsResponseChair `json:"chairs"`
	RetrievedAt int64                             `json:"retrieved_at"`
}

type appGetNearbyChairsResponseChair struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Model             string     `json:"model"`
	CurrentCoordinate Coordinate `json:"current_coordinate"`
}

// GET /api/app/nearby-chairs
func appGetNearbyChairs(w http.ResponseWriter, r *http.Request) {
	latStr := r.URL.Query().Get("latitude")
	lonStr := r.URL.Query().Get("longitude")
	distanceStr := r.URL.Query().Get("distance")
	if latStr == "" || lonStr == "" {
		writeError(w, http.StatusBadRequest, errors.New("latitude or longitude is empty"))
		return
	}

	lat, err := strconv.Atoi(latStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("latitude is invalid"))
		return
	}

	lon, err := strconv.Atoi(lonStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("longitude is invalid"))
		return
	}

	distance := 50
	if distanceStr != "" {
		distance, err = strconv.Atoi(distanceStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("distance is invalid"))
			return
		}
	}

	nearbyChairs := globalChairManager.GetNearbyChairs(lat, lon, distance)

	writeJSON(w, http.StatusOK, &appGetNearbyChairsResponse{
		Chairs:      nearbyChairs,
		RetrievedAt: time.Now().UnixMilli(),
	})
}

func calculateFare(pickupLatitude, pickupLongitude, destLatitude, destLongitude int) int {
	meteredFare := farePerDistance * calculateDistance(pickupLatitude, pickupLongitude, destLatitude, destLongitude)
	return initialFare + meteredFare
}

func calculateDiscountedFare(ctx context.Context, q queryGetter, userID string, ride *Ride, pickupLatitude, pickupLongitude, destLatitude, destLongitude int) (int, error) {
	discount := 0
	if ride != nil {
		destLatitude = ride.DestinationLatitude
		destLongitude = ride.DestinationLongitude
		pickupLatitude = ride.PickupLatitude
		pickupLongitude = ride.PickupLongitude

		// すでにクーポンが紐づいているならそれの割引額を参照
		if coupon, err := couponRepository.GetByUsedBy(ctx, q, ride.ID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
		} else {
			discount = coupon.Discount
		}
	} else {
		// 初回利用クーポンを最優先で使う
		if coupon, err := couponRepository.GetUnusedNewUserCoupon(ctx, q, userID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}

			// 無いなら他のクーポンを付与された順番に使う
			if coupon, err := couponRepository.GetOldestUnused(ctx, q, userID); err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					return 0, err
				}
			} else {
				discount = coupon.Discount
			}
		} else {
			discount = coupon.Discount
		}
	}

	meteredFare := farePerDistance * calculateDistance(pickupLatitude, pickupLongitude, destLatitude, destLongitude)
	discountedMeteredFare := max(meteredFare-discount, 0)

	return initialFare + discountedMeteredFare, nil
}

func calculateFareWithDiscount(pickupLatitude, pickupLongitude, destLatitude, destLongitude, discount int) int {
	return initialFare + max(farePerDistance*calculateDistance(pickupLatitude, pickupLongitude, destLatitude, destLongitude)-discount, 0)
}
