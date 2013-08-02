package expirator

import (
	"encoding/gob"
	"github.com/golang/glog"
	"os"
	"time"
)

type ExpirableID string

type ExpirationHandle struct {
	ExpirationTime  time.Time
	ID              ExpirableID
	expirationTimer *time.Timer
}

type Expirator struct {
	Store ExpirableStore

	dataPath            string
	expirationMap       map[ExpirableID]*ExpirationHandle
	expirationChannel   chan *ExpirationHandle
	flushRequired       bool
	urgentFlushRequired bool
}

type Expirable interface {
	ExpirationID() ExpirableID
}

type ExpirableStore interface {
	Get(ExpirableID) (Expirable, error)
	Destroy(Expirable)
}

func NewExpirator(path string, store ExpirableStore) *Expirator {
	return &Expirator{
		Store:             store,
		dataPath:          path,
		expirationChannel: make(chan *ExpirationHandle, 1000),
	}
}

func (e *Expirator) canSave() bool {
	return e.dataPath != ""
}

func (e *Expirator) loadExpirations() {
	if !e.canSave() {
		return
	}

	file, err := os.Open(e.dataPath)
	if err != nil {
		return
	}

	gobDecoder := gob.NewDecoder(file)
	tempMap := make(map[ExpirableID]*ExpirationHandle)
	gobDecoder.Decode(&tempMap)
	file.Close()

	for _, v := range tempMap {
		e.registerExpirationHandle(v)
	}
	glog.Info("Loaded ", len(tempMap), " expirations.")
}

func (e *Expirator) saveExpirations() {
	if !e.canSave() {
		return
	}

	if e.expirationMap == nil {
		return
	}

	file, err := os.Create(e.dataPath)
	if err != nil {
		glog.Error("Error writing expiration data: ", err.Error())
		return
	}

	gobEncoder := gob.NewEncoder(file)
	gobEncoder.Encode(e.expirationMap)

	file.Close()
	glog.Info("Wrote ", len(e.expirationMap), " expirations.")

	e.flushRequired, e.urgentFlushRequired = false, false
}

func (e *Expirator) registerExpirationHandle(ex *ExpirationHandle) {
	expiryFunc := func() { e.expirationChannel <- ex }

	if e.expirationMap == nil {
		e.expirationMap = make(map[ExpirableID]*ExpirationHandle)
	}

	if ex.expirationTimer != nil {
		e.cancelExpirationHandle(ex)
		glog.Info("Existing expiration for ", ex.ID, " cancelled")
	}

	now := time.Now()
	if ex.ExpirationTime.After(now) {
		e.expirationMap[ex.ID] = ex
		e.urgentFlushRequired = true

		ex.expirationTimer = time.AfterFunc(ex.ExpirationTime.Sub(now), expiryFunc)
		glog.Info("Registered expiration for ", ex.ID, " at ", ex.ExpirationTime)
	} else {
		glog.Warning("Force-expiring handle ", ex.ID, ", outdated by ", now.Sub(ex.ExpirationTime), ".")
		expiryFunc()
	}
}

func (e *Expirator) cancelExpirationHandle(ex *ExpirationHandle) {
	ex.expirationTimer.Stop()
	delete(e.expirationMap, ex.ID)
	e.urgentFlushRequired = true

	glog.Info("Execution order for ", ex.ID, " at ", ex.ExpirationTime, " belayed.")
}

func (e *Expirator) Run() {
	go e.loadExpirations()
	glog.Info("Launching Expirator.")
	var flushTickerChan, urgentFlushTickerChan <-chan time.Time
	if e.canSave() {
		flushTickerChan, urgentFlushTickerChan = time.NewTicker(30*time.Second).C, time.NewTicker(1*time.Second).C
	}
	for {
		select {
		// 30-second flush timer (only save if changed)
		case _ = <-flushTickerChan:
			if e.expirationMap != nil && (e.flushRequired || e.urgentFlushRequired) {
				e.saveExpirations()
			}
		// 1-second flush timer (only save if *super-urgent, but still throttle)
		case _ = <-urgentFlushTickerChan:
			if e.expirationMap != nil && e.urgentFlushRequired {
				e.saveExpirations()
			}
		case expiration := <-e.expirationChannel:
			glog.Info("Expiring ", expiration.ID)
			expirable, _ := e.Store.Get(expiration.ID)
			if expirable != nil {
				e.Store.Destroy(expirable)
			}

			delete(e.expirationMap, expiration.ID)
			e.flushRequired = true
		}
	}
}

func (e *Expirator) ExpireObject(ex Expirable, dur time.Duration) {
	id := ex.ExpirationID()
	exh, ok := e.expirationMap[id]
	if !ok {
		exh = &ExpirationHandle{ID: id}
	}
	exh.ExpirationTime = time.Now().Add(dur)
	e.registerExpirationHandle(exh)
}

func (e *Expirator) CancelObjectExpiration(ex Expirable) {
	id := ex.ExpirationID()
	exh, ok := e.expirationMap[id]
	if ok {
		e.cancelExpirationHandle(exh)
	}
}

func (e *Expirator) ObjectHasExpiration(ex Expirable) bool {
	id := ex.ExpirationID()
	_, ok := e.expirationMap[id]
	return ok
}