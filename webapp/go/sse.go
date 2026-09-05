package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSEStream は SSE の低レベル framing（ヘッダ・Flush・切断検知）をカプセル化する。
type SSEStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

// NewSSEStream は SSE 用ヘッダを設定し、ヘッダを即時 Flush する。
// 失敗時は false を返すので、呼び出し側でエラーレスポンスを返すこと。
func NewSSEStream(w http.ResponseWriter, r *http.Request) (*SSEStream, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return nil, false
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEStream{w: w, flusher: flusher, ctx: r.Context()}, true
}

// Send は data を `data: <json>\n\n` 形式で1イベント送信する。
func (s *SSEStream) Send(data any) error {
	buf, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", string(buf)); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Done はクライアント切断時に閉じるチャネルを返す。
func (s *SSEStream) Done() <-chan struct{} {
	return s.ctx.Done()
}

// AwaitWake は起床シグナル・切断・安全網タイムアウトのいずれかまで待つ。
// 切断時は false を返す。
func (s *SSEStream) AwaitWake(wake <-chan struct{}, d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-wake:
		return true
	case <-time.After(d):
		return true
	}
}

const (
	// sseSafetyPollInterval は起床シグナルを取りこぼしても3秒以内の
	// 通知要件を満たすための安全網ポーリング間隔。
	sseSafetyPollInterval = time.Second
	sseUnsentBatchSize    = 20
)

// wakeHub はライド状態遷移の起床通知を椅子・ユーザー単位で配信する。
// SSEループはDBポーリングの代わりにこのシグナルで起床するため、
// アイドル時の通知系QPSを 1/50ms → 1/s + 遷移回数に削減できる。
// シグナルは合流型（buffered size 1・非ブロッキング）で、
// 取りこぼしは安全網ポーリングが拾う。
type wakeHub struct {
	mu    sync.Mutex
	chair map[string]map[chan struct{}]struct{}
	user  map[string]map[chan struct{}]struct{}
}

var globalWakeHub = &wakeHub{
	chair: make(map[string]map[chan struct{}]struct{}),
	user:  make(map[string]map[chan struct{}]struct{}),
}

func (h *wakeHub) subscribe(m map[string]map[chan struct{}]struct{}, id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	set, ok := m[id]
	if !ok {
		set = make(map[chan struct{}]struct{})
		m[id] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()
	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := m[id]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(m, id)
			}
		}
	}
	return ch, unsub
}

func (h *wakeHub) broadcast(m map[string]map[chan struct{}]struct{}, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range m[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// subscribeChair は椅子向け起床シグナルを購読する。unsub は接続終了時に必ず呼ぶこと。
func subscribeChair(chairID string) (<-chan struct{}, func()) {
	return globalWakeHub.subscribe(globalWakeHub.chair, chairID)
}

// subscribeUser はユーザー向け起床シグナルを購読する。unsub は接続終了時に必ず呼ぶこと。
func subscribeUser(userID string) (<-chan struct{}, func()) {
	return globalWakeHub.subscribe(globalWakeHub.user, userID)
}

// WakeChair は椅子向け通知SSEループを起床させる。コミット後に呼ぶこと。
func WakeChair(chairID string) {
	globalWakeHub.broadcast(globalWakeHub.chair, chairID)
}

// WakeUser はユーザー向け通知SSEループを起床させる。コミット後に呼ぶこと。
func WakeUser(userID string) {
	globalWakeHub.broadcast(globalWakeHub.user, userID)
}

// StreamRideNotifications は通知ストリームの共通ポーリングループをカプセル化する。
//
//   - fetchUnsent: 未送信ステータスを時系列昇順で返す
//   - fetchLatest: 接続直後の即時送信のための最新 (ride, status) を返す。ライド未存在時は sql.ErrNoRows を返す
//   - fetchRide: rideID からライドを取得する
//   - build: ride と status から通知ペイロードを組み立てる
//   - markSent: 送信済みマークを付ける
//   - wake: 状態遷移時の起床シグナル。なければ安全網ポーリングのみで待つ
//
// すべての状態遷移を発生順に少なくとも1回以上返し、状態変更から3秒以内の通知を満たす。
// アイドル時はDBを叩かず、起床または安全網間隔でのみポーリングする。
func StreamRideNotifications(
	s *SSEStream,
	fetchUnsent func(ctx context.Context) ([]RideStatus, error),
	fetchLatest func(ctx context.Context) (*Ride, string, error),
	fetchRide func(ctx context.Context, rideID string) (*Ride, error),
	build func(ctx context.Context, ride *Ride, status string) (any, error),
	markSent func(ctx context.Context, id string) error,
	wake <-chan struct{},
) {
	ctx := s.ctx
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		unsent, err := fetchUnsent(ctx)
		if err != nil {
			// DBエラー時はホットスピンを避けて待機してからリトライする
			if !s.AwaitWake(wake, sseSafetyPollInterval) {
				return
			}
			continue
		}

		if len(unsent) > 0 {
			for _, rs := range unsent {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ride, err := fetchRide(ctx, rs.RideID)
				if err != nil {
					continue
				}
				data, err := build(ctx, ride, rs.Status)
				if err != nil {
					continue
				}
				if err := s.Send(data); err != nil {
					return
				}
				_ = markSent(ctx, rs.ID)
			}
			first = false
			continue
		}

		if first {
			ride, status, err := fetchLatest(ctx)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					// ライド未存在（割当前など）は起床または安全網で再試行
					if !s.AwaitWake(wake, sseSafetyPollInterval) {
						return
					}
					continue
				}
				if !s.AwaitWake(wake, sseSafetyPollInterval) {
					return
				}
				continue
			}
			if data, err := build(ctx, ride, status); err == nil {
				if err := s.Send(data); err != nil {
					return
				}
			}
			first = false
		}

		if !s.AwaitWake(wake, sseSafetyPollInterval) {
			return
		}
	}
}
