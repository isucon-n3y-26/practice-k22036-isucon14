package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// Wait は切断されるか d が経過するまで待つ。切断時は false を返す。
func (s *SSEStream) Wait(d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

const (
	ssePollInterval       = 50 * time.Millisecond
	sseEmptyRetryInterval = 100 * time.Millisecond
	sseUnsentBatchSize    = 20
)

// StreamRideNotifications は通知ストリームの共通ポーリングループをカプセル化する。
//
//   - fetchUnsent: 未送信ステータスを時系列昇順で返す
//   - fetchLatest: 接続直後の即時送信のための最新 (ride, status) を返す。ライド未存在時は sql.ErrNoRows を返す
//   - fetchRide: rideID からライドを取得する
//   - build: ride と status から通知ペイロードを組み立てる
//   - markSent: 送信済みマークを付ける
//
// すべての状態遷移を発生順に少なくとも1回以上返し、状態変更から3秒以内の通知を満たす。
func StreamRideNotifications(
	s *SSEStream,
	fetchUnsent func(ctx context.Context) ([]RideStatus, error),
	fetchLatest func(ctx context.Context) (*Ride, string, error),
	fetchRide func(ctx context.Context, rideID string) (*Ride, error),
	build func(ctx context.Context, ride *Ride, status string) (any, error),
	markSent func(ctx context.Context, id string) error,
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
			select {
			case <-ctx.Done():
				return
			default:
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
					// ライド未存在（割当前など）は待機してリトライ
					if !s.Wait(sseEmptyRetryInterval) {
						return
					}
					continue
				}
				if !s.Wait(ssePollInterval) {
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

		if !s.Wait(ssePollInterval) {
			return
		}
	}
}
