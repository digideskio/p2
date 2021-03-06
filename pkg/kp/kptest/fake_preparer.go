package kptest

import (
	"sync"
	"time"

	"github.com/square/p2/pkg/kp"
	"github.com/square/p2/pkg/logging"
)

const (
	checkRate = time.Duration(100 * time.Millisecond)
)

type FakePreparer struct {
	podStore *FakePodStore

	enabled        bool
	enableLock     sync.Mutex
	preparerQuitCh chan struct{}
	logger         logging.Logger
}

func NewFakePreparer(podStore *FakePodStore, logger logging.Logger) *FakePreparer {
	return &FakePreparer{
		podStore: podStore,
		enabled:  false,
		logger:   logger,
	}
}

func (f *FakePreparer) Enable() {
	f.enableLock.Lock()
	defer f.enableLock.Unlock()

	if f.enabled {
		return
	}
	f.enabled = true
	f.preparerQuitCh = make(chan struct{})

	go func() {
		for {
			select {
			case <-f.preparerQuitCh:
				return
			case <-time.Tick(checkRate):
				// Get all pods
				allPods, _, err := f.podStore.AllPods(kp.INTENT_TREE)
				if err != nil {
					f.logger.Errorf("Error getting all pods: %v", err)
					continue
				}
				// Make a copy to know what to delete
				podsToDelete := make(map[kp.ManifestResult]struct{})
				for _, manifestResult := range allPods {
					podsToDelete[manifestResult] = struct{}{}
				}
				// Set pods that are in intent
				for _, manifestResult := range allPods {
					_, err = f.podStore.SetPod(
						kp.REALITY_TREE,
						manifestResult.PodLocation.Node,
						manifestResult.Manifest,
					)
					if err != nil {
						f.logger.Errorf("Error setting pod: %v", err)
					}
					delete(podsToDelete, manifestResult)
				}
				// Delete pods that are missing from intent
				for manifestResult := range podsToDelete {
					_, err = f.podStore.DeletePod(
						kp.REALITY_TREE,
						manifestResult.PodLocation.Node,
						manifestResult.PodLocation.PodID,
					)
					if err != nil {
						f.logger.Errorf("Error deleting pod: %v", err)
					}
				}
			}
		}
	}()
}

func (f *FakePreparer) Disable() {
	f.enableLock.Lock()
	defer f.enableLock.Unlock()

	if !f.enabled {
		return
	}

	f.enabled = false
	close(f.preparerQuitCh)
}
