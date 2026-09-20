package main

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/cache"
	"github.com/isucon/isucon14/webapp/go/repository"
)

type fanoutHandler struct {
	handlers []slog.Handler
}

func (f *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r)
		}
	}
	return nil
}

func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		newHandlers[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: newHandlers}
}

func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		newHandlers[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: newHandlers}
}

var db *sqlx.DB
var userRepository *repository.UserRepository
var rideRepository *repository.RideRepository
var rideStatusRepository *repository.RideStatusRepository
var chairRepository *repository.ChairRepository
var matchingQueueRepository *repository.MatchingQueueRepository
var couponRepository *repository.CouponRepository
var ownerRepository *repository.OwnerRepository
var paymentTokenRepository *repository.PaymentTokenRepository
var settingsRepository *repository.SettingsRepository
var chairModelRepository *repository.ChairModelRepository
var chairLocationRepository *repository.ChairLocationRepository
var globalStatusCache *cache.StatusCache
var globalRideCoordsCache *cache.RideCoordsCache
var globalLocationBuffer *LocationBuffer
var globalDistanceBuffer *DistanceBuffer
var globalSentMarkBuffer *SentMarkBuffer
var globalStatusLog *StatusLog
var matcherStarted bool

// paymentGatewayBaseURL は決済サーバのURL。初期化時に設定され、以後不変。
// 評価ごとの settings 取得排除用。
var paymentGatewayBaseURL string

func main() {
	configureLogging()
	mux := setup()
	srv := &http.Server{Addr: ":8080", Handler: mux}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()
	go func() {
		slog.Info("Listening on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()
	<-ctx.Done()
	// 再起動・停止時の滞留消失を防ぐため、バッファを吐き出してから終了する
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	globalLocationBuffer.Flush(shutdownCtx)
	globalDistanceBuffer.Flush(shutdownCtx)
	globalSentMarkBuffer.Flush(shutdownCtx)
}

func configureLogging() {
	level := slog.LevelInfo
	switch os.Getenv("ISUCON_LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logFile := os.Getenv("ISUCON_LOG_FILE")
	errorFile := os.Getenv("ISUCON_LOG_ERROR_FILE")

	var out *os.File
	var err error

	if logFile != "" {
		out, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", logFile, err)
			os.Exit(1)
		}
	} else {
		out = os.Stdout
	}

	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))

	if errorFile != "" {
		errOut, err := os.OpenFile(errorFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			slog.Error("Failed to open error log file", "file", errorFile, "error", err)
			os.Exit(1)
		}
		// ERROR以上を errorFile にも出力するハンドラを追加
		errorHandler := slog.NewJSONHandler(errOut, &slog.HandlerOptions{
			Level: slog.LevelError,
		})
		slog.SetDefault(slog.New(&fanoutHandler{handlers: []slog.Handler{handler, errorHandler}}))
	}
}

func setup() http.Handler {
	host := os.Getenv("ISUCON_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("ISUCON_DB_PORT")
	if port == "" {
		port = "3306"
	}
	_, err := strconv.Atoi(port)
	if err != nil {
		panic(fmt.Sprintf("failed to convert DB port number from ISUCON_DB_PORT environment variable into int: %v", err))
	}
	user := os.Getenv("ISUCON_DB_USER")
	if user == "" {
		user = "isucon"
	}
	password := os.Getenv("ISUCON_DB_PASSWORD")
	if password == "" {
		password = "isucon"
	}
	dbname := os.Getenv("ISUCON_DB_NAME")
	if dbname == "" {
		dbname = "isuride"
	}

	dbConfig := mysql.NewConfig()
	dbConfig.User = user
	dbConfig.Passwd = password
	dbConfig.Addr = net.JoinHostPort(host, port)
	dbConfig.Net = "tcp"
	dbConfig.DBName = dbname
	dbConfig.ParseTime = true
	// プレースホルダをクライアント側で展開し、サーバーサイドプリペアド
	// （COM_STMT_PREPARE＋EXECUTEの2往復）をなくす。起動直後の
	// Prepareバースト解消とクエリ毎の往復削減のため。
	dbConfig.InterpolateParams = true

	_db, err := sqlx.Connect("mysql", dbConfig.FormatDSN())
	if err != nil {
		panic(err)
	}
	db = _db
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(100)
	db.SetConnMaxLifetime(2 * time.Minute)
	userRepository = repository.NewUserRepository(db)
	rideRepository = repository.NewRideRepository(db)
	rideStatusRepository = repository.NewRideStatusRepository(db)
	chairRepository = repository.NewChairRepository(db)
	matchingQueueRepository = repository.NewMatchingQueueRepository(db)
	couponRepository = repository.NewCouponRepository(db)
	ownerRepository = repository.NewOwnerRepository(db)
	paymentTokenRepository = repository.NewPaymentTokenRepository(db)
	settingsRepository = repository.NewSettingsRepository(db)
	chairModelRepository = repository.NewChairModelRepository(db)
	chairLocationRepository = repository.NewChairLocationRepository(db)
	globalLocationBuffer = NewLocationBuffer(db)
	go globalLocationBuffer.Start(context.Background())
	globalSentMarkBuffer = NewSentMarkBuffer(db)
	go globalSentMarkBuffer.Start(context.Background())
	// DistanceBuffer は共有プールとは別の専用プール（2本）で書込む。
	// flush が他クエリの混雑によるプール枯渇待ちに巻き込まれると
	// updated_at が停滞し、total_distance 鮮度検証に触れる。
	// 座標POST自体はプール不要のため ServerTime だけが進み、
	// 非対称な停滞になる点に注意（共有プールでは起きない）。
	distanceDB, err := sqlx.Connect("mysql", dbConfig.FormatDSN())
	if err != nil {
		panic(err)
	}
	distanceDB.SetMaxOpenConns(2)
	distanceDB.SetMaxIdleConns(2)
	distanceDB.SetConnMaxLifetime(2 * time.Minute)
	globalDistanceBuffer = NewDistanceBuffer(distanceDB)
	go globalDistanceBuffer.Start(context.Background())
	globalStatusLog = NewStatusLog()
	// 決済URLは起動時に読み込み、初期化APIで更新する。以後不変のためキャッシュする。
	if url, err := settingsRepository.GetPaymentGatewayURL(context.Background(), db); err != nil {
		slog.Warn("failed to load payment_gateway_url, will be set on initialize", "error", err)
	} else {
		paymentGatewayBaseURL = url
	}
	globalStatusCache = cache.NewStatusCache(func(ctx context.Context, q cache.Getter, rideID string) (string, error) {
		return rideStatusRepository.GetLatestStatusByRideID(ctx, q, rideID)
	})
	globalRideCoordsCache = cache.NewRideCoordsCache(func(ctx context.Context, rideID string) (cache.RideCoords, error) {
		ride, err := rideRepository.GetByID(ctx, db, rideID)
		if err != nil {
			return cache.RideCoords{}, err
		}
		return cache.RideCoords{
			RideID:               ride.ID,
			UserID:               ride.UserID,
			PickupLatitude:       ride.PickupLatitude,
			PickupLongitude:      ride.PickupLongitude,
			DestinationLatitude:  ride.DestinationLatitude,
			DestinationLongitude: ride.DestinationLongitude,
		}, nil
	})

	if err := globalChairManager.Reload(context.Background(), db); err != nil {
		panic(err)
	}
	if err := globalDistanceBuffer.SyncFromDB(context.Background()); err != nil {
		panic(err)
	}
	if err := reloadChairStats(context.Background(), db); err != nil {
		panic(err)
	}
	if err := reloadStatusLog(context.Background()); err != nil {
		panic(err)
	}

	// matcherは起動時にも必ず起動する。initialize時のみだと、
	// bench中の再起動でマッチングが停止したままになる。
	// 二重起動防止は matcherStarted で行う。
	if !matcherStarted {
		matcherStarted = true
		slog.Info("starting matcher after setup")
		go startMatcher(context.Background())
	}

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	mux.Use(middleware.Recoverer)
	mux.HandleFunc("POST /api/initialize", postInitialize)

	// app handlers
	{
		mux.HandleFunc("POST /api/app/users", appPostUsers)

		authedMux := mux.With(appAuthMiddleware)
		authedMux.HandleFunc("POST /api/app/payment-methods", appPostPaymentMethods)
		authedMux.HandleFunc("GET /api/app/rides", appGetRides)
		authedMux.HandleFunc("POST /api/app/rides", appPostRides)
		authedMux.HandleFunc("POST /api/app/rides/estimated-fare", appPostRidesEstimatedFare)
		authedMux.HandleFunc("POST /api/app/rides/{ride_id}/evaluation", appPostRideEvaluatation)
		authedMux.HandleFunc("GET /api/app/notification", appGetNotification)
		authedMux.HandleFunc("GET /api/app/nearby-chairs", appGetNearbyChairs)
	}

	// owner handlers
	{
		mux.HandleFunc("POST /api/owner/owners", ownerPostOwners)

		authedMux := mux.With(ownerAuthMiddleware)
		authedMux.HandleFunc("GET /api/owner/sales", ownerGetSales)
		authedMux.HandleFunc("GET /api/owner/chairs", ownerGetChairs)
	}

	// chair handlers
	{
		mux.HandleFunc("POST /api/chair/chairs", chairPostChairs)

		authedMux := mux.With(chairAuthMiddleware)
		authedMux.HandleFunc("POST /api/chair/activity", chairPostActivity)
		authedMux.HandleFunc("POST /api/chair/coordinate", chairPostCoordinate)
		authedMux.HandleFunc("GET /api/chair/notification", chairGetNotification)
		authedMux.HandleFunc("POST /api/chair/rides/{ride_id}/status", chairPostRideStatus)
	}

	// internal handlers
	{
		mux.HandleFunc("GET /api/internal/matching", internalGetMatching)
	}

	return mux
}

type postInitializeRequest struct {
	PaymentServer string `json:"payment_server"`
}

type postInitializeResponse struct {
	Language string `json:"language"`
}

// reloadStatusLog は未送信行をDBから未送信ログへ復元する。
// 起動時・初期化 billet用。呼出し前に Clear すること。
func reloadStatusLog(ctx context.Context) error {
	rows, err := rideStatusRepository.ListUnsent(ctx, db)
	if err != nil {
		return err
	}
	globalStatusLog.Backfill(rows)
	return nil
}

func postInitialize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &postInitializeRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to initialize: %s: %w", string(out), err))
		return
	}

	if err := settingsRepository.UpdatePaymentGatewayURL(ctx, db, req.PaymentServer); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paymentGatewayBaseURL = req.PaymentServer

	if err := globalChairManager.Reload(ctx, db); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := reloadChairStats(ctx, db); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// DB初期化で全データが破棄されるため、各種キャッシュもクリアする
	globalStatusCache.Clear()
	globalRideCoordsCache.Clear()
	globalLocationBuffer.Discard()
	globalSentMarkBuffer.Discard()
	globalDistanceBuffer.Discard()
	if err := globalDistanceBuffer.SyncFromDB(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	globalStatusLog.Clear()
	if err := reloadStatusLog(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	userRepository.ClearCache()
	chairRepository.ClearCache()
	ownerRepository.ClearCache()

	// キューに積まれていない未割当MATCHINGライドを救済登録する
	// （通常は空のはずだが、再起動時などの取りこぼし対策）
	if pending, err := rideRepository.GetUnassignedMatchingRides(ctx, db); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else {
		for _, ride := range pending {
			if err := matchingQueueRepository.EnqueueIfMissing(ctx, db, ride.ID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
	}

	if !matcherStarted {
		matcherStarted = true
		slog.Info("starting matcher after initialization")
		go startMatcher(context.Background())
	}

	triggerMatching()

	writeJSON(w, http.StatusOK, postInitializeResponse{Language: "go"})
}

type Coordinate struct {
	Latitude  int `json:"latitude"`
	Longitude int `json:"longitude"`
}

func bindJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	buf, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	w.Write(buf)
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(statusCode)
	buf, marshalError := json.Marshal(map[string]string{"message": err.Error()})
	if marshalError != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"marshaling error failed"}`))
		return
	}
	w.Write(buf)

	slog.Error("error response wrote", "error", err)
}

func secureRandomStr(b int) string {
	k := make([]byte, b)
	if _, err := crand.Read(k); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", k)
}
