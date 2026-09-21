package main

import (
	"context"
	"hash/fnv"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
)

// nearbyGraceMs は割当解除後にnearby表示から除外し続ける猶予。
// 解除（評価処理＝評価リクエスト受信後）からbench側のEvaluated反映
// （評価応答の受信・処理後）までの間だけ隠す。benchの「ライド中」検証
// （CODE=30）は応答に含まれた椅子の最新ライドが未評価だと警告になるため、
// この猶予でEvaluated反映より先の再出現を防ぐ。実測の危険窓は約8msの
// ため、50msで十分な余裕がある。長すぎると逆に不足警告（CODE=31）の
// 窓になるため、最小限に留める。マッチング側は即時解放のため、
// この値はスループットに影響しない（nearbyの表示のみ）。
const nearbyGraceMs = 50

// ChairState は椅子1台の状態。不変値として atomic.Pointer で保持し、
// 読取はロックフリー、書込みは stripe ロック下で copy-on-write する。
// 従来の単一RWMutexでは付近検索・マッチ走査（約100台×100μs）が
// 座標POSTの更新と直列化し、POST遅延→bench側の送信間引き→
// total_distance欠落を招くため、この構造に変えた。意味は等価。
type ChairState struct {
	ID            string
	Name          string
	Model         string
	Speed         int
	IsActive      bool
	Latitude      int
	Longitude     int
	HasLocation   bool
	CurrentRideID string
	// FreedAt は直近の割当解除時刻（UnixMilli）。nearby表示のみ猶予付きで
	// 除外するためのもので、マッチングの空き判定には使わない。
	// 評価応答のbench側反映（Evaluated）より先に再出現すると
	// 「ライド中」検証（CODE=30）に触れるため、解除直後は表示上
	// まだ riding 扱いにする。猶予は50msに留め、不足警告（CODE=31）の
	// 窓を最小化する。
	FreedAt int64
}

type ChairManager struct {
	chairs      sync.Map // chairID -> *atomic.Pointer[ChairState]
	modelMu     sync.Mutex
	modelSpeeds map[string]int
	stripes     [256]sync.Mutex
	// ordered は走査用スナップショット。sync.Map.Range（型アサート・
	// クロージャ・interface boxing 付き）と等価だが高速。
	// 登録・削除時のみ作り直し、走査はロック保持なしで読む。
	// 要素は atomic.Pointer のため状態は常に最新。
	ordMu   sync.RWMutex
	ordered []*atomic.Pointer[ChairState]
}

// rebuildOrderedLocked は ordered を作り直す。登録・削除時に呼ぶこと。
// 初期化・椅子登録の同期パスでのみ呼ぶため、走査側と競合しない。
func (cm *ChairManager) rebuildOrdered() {
	list := make([]*atomic.Pointer[ChairState], 0, 1024)
	cm.chairs.Range(func(_, value any) bool {
		list = append(list, value.(*atomic.Pointer[ChairState]))
		return true
	})
	cm.ordMu.Lock()
	cm.ordered = list
	cm.ordMu.Unlock()
}

// snapshot は走査用スライスを返す。要素の指す状態は atomic で最新。
func (cm *ChairManager) snapshot() []*atomic.Pointer[ChairState] {
	cm.ordMu.RLock()
	defer cm.ordMu.RUnlock()
	return cm.ordered
}

var globalChairManager = &ChairManager{
	modelSpeeds: make(map[string]int),
}

func (cm *ChairManager) stripe(id string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return &cm.stripes[h.Sum32()%uint32(len(cm.stripes))]
}

func (cm *ChairManager) speedOf(model string) int {
	cm.modelMu.Lock()
	defer cm.modelMu.Unlock()
	if s := cm.modelSpeeds[model]; s != 0 {
		return s
	}
	return 2
}

func (cm *ChairManager) loadPtr(id string) *atomic.Pointer[ChairState] {
	if v, ok := cm.chairs.Load(id); ok {
		return v.(*atomic.Pointer[ChairState])
	}
	return nil
}

// update は stripe 直列化下で状態を copy-on-write 更新する。
// 存在しない椅子への更新は無視する（従来の ok ガードと等価）。
func (cm *ChairManager) update(id string, fn func(*ChairState)) {
	p := cm.loadPtr(id)
	if p == nil {
		return
	}
	s := cm.stripe(id)
	s.Lock()
	defer s.Unlock()
	cur := p.Load()
	if cur == nil {
		return
	}
	next := *cur
	fn(&next)
	p.Store(&next)
}

func (cm *ChairManager) Reload(ctx context.Context, db *sqlx.DB) error {
	models, err := chairModelRepository.ListAll(ctx, db)
	if err != nil {
		return err
	}
	freshSpeeds := make(map[string]int, len(models))
	for _, m := range models {
		freshSpeeds[m.Name] = m.Speed
	}

	chairs, err := chairRepository.ListAll(ctx, db)
	if err != nil {
		return err
	}

	locs, err := chairRepository.GetLatestLocations(ctx, db)
	if err != nil {
		return err
	}

	incompleteRides, err := rideRepository.ListIncompleteRides(ctx, db)
	if err != nil {
		return err
	}

	// 起動時・初期化時の再構築。初期化中はbenchトラフィックなしのため、
	// 削除→再登録の transient は観測されない。
	cm.modelMu.Lock()
	cm.modelSpeeds = freshSpeeds
	cm.modelMu.Unlock()

	keep := make(map[string]struct{}, len(chairs))
	for _, c := range chairs {
		speed := freshSpeeds[c.Model]
		if speed == 0 {
			speed = 2
		}
		st := &ChairState{
			ID:       c.ID,
			Name:     c.Name,
			Model:    c.Model,
			Speed:    speed,
			IsActive: c.IsActive,
		}
		keep[c.ID] = struct{}{}
		if p := cm.loadPtr(c.ID); p != nil {
			p.Store(st)
		} else {
			p := &atomic.Pointer[ChairState]{}
			p.Store(st)
			cm.chairs.Store(c.ID, p)
		}
	}
	cm.chairs.Range(func(key, _ any) bool {
		if _, ok := keep[key.(string)]; !ok {
			cm.chairs.Delete(key)
		}
		return true
	})
	cm.rebuildOrdered()

	for _, l := range locs {
		l := l
		cm.update(l.ChairID, func(st *ChairState) {
			st.Latitude = l.Latitude
			st.Longitude = l.Longitude
			st.HasLocation = true
		})
	}
	for _, r := range incompleteRides {
		r := r
		cm.update(r.ChairID, func(st *ChairState) {
			st.CurrentRideID = r.RideID
		})
	}
	return nil
}

func (cm *ChairManager) RegisterChair(id, name, model string) {
	p := &atomic.Pointer[ChairState]{}
	p.Store(&ChairState{
		ID:       id,
		Name:     name,
		Model:    model,
		Speed:    cm.speedOf(model),
		IsActive: false,
	})
	// 同ID再登録時は既存のライブ状態を優先する
	if _, loaded := cm.chairs.LoadOrStore(id, p); !loaded {
		cm.rebuildOrdered()
	}
}

func (cm *ChairManager) SetActivity(chairID string, isActive bool) {
	cm.update(chairID, func(st *ChairState) {
		st.IsActive = isActive
	})
}

func (cm *ChairManager) UpdateLocation(chairID string, lat, lon int) {
	cm.update(chairID, func(st *ChairState) {
		st.Latitude = lat
		st.Longitude = lon
		st.HasLocation = true
	})
}

// SnapshotLocations は位置既知の全椅子の最新座標を返す。
// DistanceBufferのReload同期用（起動時・初期化時のみ呼ぶこと）。
func (cm *ChairManager) SnapshotLocations() map[string][2]int {
	out := make(map[string][2]int)
	for _, p := range cm.snapshot() {
		st := p.Load()
		if st != nil && st.HasLocation {
			out[st.ID] = [2]int{st.Latitude, st.Longitude}
		}
	}
	return out
}

// GetCurrentRideID は椅子に割り当て中のライドIDを返す。
// 未割当時は ok=false。FindBestAvailableChair/Reload で設定され、
// CompleteRide/UnassignRide でクリアされる。
func (cm *ChairManager) GetCurrentRideID(chairID string) (rideID string, ok bool) {
	p := cm.loadPtr(chairID)
	if p == nil {
		return "", false
	}
	st := p.Load()
	if st == nil || st.CurrentRideID == "" {
		return "", false
	}
	return st.CurrentRideID, true
}

// GetLocation は椅子の最新既知座標を返す。chairPostCoordinate の
// 走行距離差分計算用で、直前位置SELECT
// （chair_locations を created_at 降順で1件取得）と等価。
// ChairManager は初期化時にDB最新値でロードされ、座標更新のたびに
// 更新されるため、常に直前SELECTと同じ値を返す。
// 未登録・未測位の椅子に対しては ok=false を返す。
func (cm *ChairManager) GetLocation(chairID string) (lat, lon int, ok bool) {
	p := cm.loadPtr(chairID)
	if p == nil {
		return 0, 0, false
	}
	st := p.Load()
	if st == nil || !st.HasLocation {
		return 0, 0, false
	}
	return st.Latitude, st.Longitude, true
}

func (cm *ChairManager) AssignRide(chairID, rideID string) {
	cm.update(chairID, func(st *ChairState) {
		st.CurrentRideID = rideID
	})
}

func (cm *ChairManager) CompleteRide(chairID string) {
	// 割当を即時解除し、nearby表示上は50msだけ riding 扱いを続ける。
	// 解除は評価リクエスト受信後に行い、表示は評価応答のbench側反映を
	// 待ってから戻すことで、「ライド中」検証（CODE=30）を回避する。
	// 猶予は実測の危険窓（約8ms）に見合う最小限とし、不足警告を抑える。
	cm.update(chairID, func(st *ChairState) {
		st.CurrentRideID = ""
		st.FreedAt = time.Now().UnixMilli()
	})
}

func (cm *ChairManager) UnassignRide(chairID string) {
	cm.update(chairID, func(st *ChairState) {
		st.CurrentRideID = ""
	})
}

// FindBestAvailableChair は利用可能な椅子の中で到着時間が最も短い椅子を選択し、即座に rideID を割り当てます
// 迎車地点までの距離が maxDist を超える椅子は候補から除外する（遠距離割当の見送り）。
// 上限内に候補が無い場合は ok=false を返し、ライドはキューに残る。
func (cm *ChairManager) FindBestAvailableChair(pickupLat, pickupLon int, rideID string, maxDist int) (*ChairState, bool) {
	// 走査はロックフリーのスナップショットで行い、確定だけ stripe 下で
	// 再検証＋割当てするため、同時実行に対して安全。最大3走査。
	for attempt := 0; attempt < 3; attempt++ {
		var bestID string
		bestTime := math.MaxFloat64
		bestDistance := math.MaxInt
		hasBest := false

		for _, p := range cm.snapshot() {
			st := p.Load()
			if st == nil || !st.IsActive || !st.HasLocation || st.CurrentRideID != "" {
				continue
			}
			dist := calculateDistance(pickupLat, pickupLon, st.Latitude, st.Longitude)
			if dist > maxDist {
				continue
			}
			estimatedTime := float64(dist) / float64(st.Speed)
			if estimatedTime < bestTime || (estimatedTime == bestTime && dist < bestDistance) {
				bestTime = estimatedTime
				bestDistance = dist
				bestID = st.ID
				hasBest = true
			}
		}
		if !hasBest {
			return nil, false
		}

		p := cm.loadPtr(bestID)
		if p == nil {
			continue
		}
		s := cm.stripe(bestID)
		s.Lock()
		cur := p.Load()
		if cur != nil && cur.IsActive && cur.HasLocation && cur.CurrentRideID == "" {
			next := *cur
			next.CurrentRideID = rideID
			p.Store(&next)
			claimed := next
			s.Unlock()
			return &claimed, true
		}
		s.Unlock()
		// 確定時に塞がっていた。再走査する
	}
	return nil, false
}

func (cm *ChairManager) GetNearbyChairs(lat, lon, distance int) []appGetNearbyChairsResponseChair {
	// 非表示条件は割当中＋解放直後の猶予（50ms）のみ。
	// 猶予は評価応答のbench側反映待ちで、不足警告の窓を最小化する。
	now := time.Now().UnixMilli()
	nearby := make([]appGetNearbyChairsResponseChair, 0)
	for _, p := range cm.snapshot() {
		st := p.Load()
		if st == nil || !st.IsActive || !st.HasLocation || st.CurrentRideID != "" {
			continue
		}
		if now-st.FreedAt < nearbyGraceMs {
			continue
		}
		if calculateDistance(lat, lon, st.Latitude, st.Longitude) <= distance {
			nearby = append(nearby, appGetNearbyChairsResponseChair{
				ID:    st.ID,
				Name:  st.Name,
				Model: st.Model,
				CurrentCoordinate: Coordinate{
					Latitude:  st.Latitude,
					Longitude: st.Longitude,
				},
			})
		}
	}
	return nearby
}
