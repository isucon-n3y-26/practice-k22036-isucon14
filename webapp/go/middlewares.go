package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// sessionToken は Cookie ヘッダから指定名の値を取り出す。
// benchは1接続1クッキー運用のため、';' を含まない単一形式を
// 高速パスで処理する（r.Cookie の全パース・alloc回避）。
// 複数クッキー時は標準パーサへフォールバックするため等価。
func sessionToken(r *http.Request, name string) (string, bool) {
	h := r.Header.Get("Cookie")
	if h == "" {
		return "", false
	}
	if strings.IndexByte(h, ';') < 0 {
		i := strings.IndexByte(h, '=')
		if i <= 0 || strings.TrimSpace(h[:i]) != name {
			return "", false
		}
		return h[i+1:], true
	}
	c, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

// accessLogger は異常系のみ記録する軽量アクセスログ。
// 全量記録は高負荷時に毎秒数千行の journal 書込みとなり、
// journald と CPU を奪い合って律速するため、エラー応答と
// 500ms超の遅延（SSE 長ポーリングの正常範囲を除く）のみ残す。
func accessLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		elapsed := time.Since(start)
		status := ww.Status()
		if status >= 400 || (elapsed > 500*time.Millisecond && !strings.HasSuffix(r.URL.Path, "/notification")) {
			slog.Warn("anomalous response",
				"method", r.Method, "path", r.URL.Path,
				"status", status, "elapsed", elapsed.String())
		}
	})
}

func appAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		accessToken, ok := sessionToken(r, "app_session")
		if !ok || accessToken == "" {
			writeError(w, http.StatusUnauthorized, errors.New("app_session cookie is required"))
			return
		}
		user, err := userRepository.GetByAccessToken(ctx, accessToken)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		ctx = context.WithValue(ctx, "user", user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ownerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		accessToken, ok := sessionToken(r, "owner_session")
		if !ok || accessToken == "" {
			writeError(w, http.StatusUnauthorized, errors.New("owner_session cookie is required"))
			return
		}
		owner, err := ownerRepository.GetByAccessToken(ctx, accessToken)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		ctx = context.WithValue(ctx, "owner", owner)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func chairAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		accessToken, ok := sessionToken(r, "chair_session")
		if !ok || accessToken == "" {
			writeError(w, http.StatusUnauthorized, errors.New("chair_session cookie is required"))
			return
		}
		chair, err := chairRepository.GetByAccessToken(ctx, accessToken)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		ctx = context.WithValue(ctx, "chair", chair)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
