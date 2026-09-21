package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// 決済GW専用クライアント。DefaultClientはhost毎idle2接続・タイムアウト無しで
// 高並行時にTCP/TLSハンドシェイク連発＋ハング蓄積になるため、プールと上限を明示する。
var paymentHTTPClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	},
}

type paymentGatewayPostPaymentRequest struct {
	Amount int `json:"amount"`
}

func requestPaymentGatewayPostPayment(ctx context.Context, paymentGatewayURL string, token string, param *paymentGatewayPostPaymentRequest, idempotencyKey string) error {
	b, err := json.Marshal(param)
	if err != nil {
		return err
	}

	// 失敗したらとりあえずリトライ
	// FIXME: 社内決済マイクロサービスのインフラに異常が発生していて、同時にたくさんリクエストすると変なことになる可能性あり
	// Idempotency-Key により再送は冪等なため、件数突合せの確認GETは廃止した。
	// 2xxは成功（204初回・200冪等リプレイ等の差異を吸収）、それ以外は再送して
	// 尽きたらエラーにする。評価POSTのbench側再送も同一キーで冪等化される。
	retry := 0
	for {
		err := func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, paymentGatewayURL+"/payments", bytes.NewBuffer(b))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			// 同一キーでの再送は冪等に扱われる。キーはライドID:
			// 1ライド1課金で再送時も同一値のため、内側リトライ×5・
			// 評価POSTのbench側再送のいずれでも二重課金にならない。
			req.Header.Set("Idempotency-Key", idempotencyKey)

			res, err := paymentHTTPClient.Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()

			if res.StatusCode < 200 || res.StatusCode >= 300 {
				return fmt.Errorf("[POST /payments] unexpected status code (%d)", res.StatusCode)
			}
			return nil
		}()
		if err != nil {
			if retry < 5 {
				retry++
				time.Sleep(100 * time.Millisecond)
				continue
			} else {
				return err
			}
		}
		break
	}

	return nil
}
