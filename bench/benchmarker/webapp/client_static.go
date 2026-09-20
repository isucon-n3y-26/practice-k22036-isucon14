package webapp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/isucon/isucon14/bench/benchrun"
)

// staticFileMaxAttempts は静的ファイル取得の試行回数。
// タスク起動直後はFargate側ENIの一過性不通で単発失敗することがあるため、
// リトライする（真の配信破損時は全滅して変わらず失敗する）。
const staticFileMaxAttempts = 3

// staticFileRetryInterval は静的ファイル取得リトライの間隔。
const staticFileRetryInterval = 5 * time.Second

func (c *Client) StaticGetFileHash(ctx context.Context, path string) (string, error) {
	var err error
	for attempt := 1; ; attempt++ {
		var hash string
		hash, err = c.staticGetFileHashOnce(ctx, path)
		if err == nil {
			return hash, nil
		}
		if attempt >= staticFileMaxAttempts {
			return "", err
		}
		slog.Warn("静的ファイルの取得をリトライします", "path", path, "attempt", attempt, "error", err.Error())
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(staticFileRetryInterval):
		}
	}
}

func (c *Client) staticGetFileHashOnce(ctx context.Context, path string) (string, error) {
	req, err := c.agent.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.agent.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("GET %sのリクエストが失敗しました: %w", path, err)
	}
	defer closeBody(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("GET %sへのリクエストに対して、期待されたHTTPステータスコードが確認できませんでした (expected:200～399, actual:%d)", path, resp.StatusCode)
	}

	hash, err := benchrun.GetHashFromStream(resp.Body)
	if err != nil {
		return "", fmt.Errorf("GET %sのレスポンスのボディの取得に失敗しました: %w", path, err)
	}

	return hash, nil
}
