// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/minio/minio/internal/logger"
	"github.com/minio/pkg/console"
)

const (
	// callhomeStartDelay = 1 * time.Minute // Time to wait on startup.
	// callhomeInterval   = 24 * time.Hour  // Time to wait between cycles.
	callhomeStartDelay = 5 * time.Second  // Time to wait on startup.
	callhomeInterval   = 20 * time.Second // Time to wait between cycles.
)

var (
	callhomeLeaderLockTimeout = newDynamicTimeout(30*time.Second, 10*time.Second)
	callhomeCycle             = &safeDuration{
		t: callhomeInterval,
	}
)

// initCallhome will start the scanner in the background.
func initCallhome(ctx context.Context, objAPI ObjectLayer) {
	fmt.Println("Inside initCallhome")
	go func() {
		fmt.Println("Sleeping for ", callhomeStartDelay)
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		// Run callhome in a loop
		for {
			fmt.Println("Going to call runCallhome")
			runCallhome(ctx, objAPI)
			duration := time.Duration(r.Float64() * float64(callhomeCycle.Get()))
			if duration < time.Second {
				// Make sure to sleep atleast a second to avoid high CPU ticks.
				duration = time.Second
			}
			fmt.Println("Going to sleep for", duration)
			time.Sleep(duration)
		}
	}()
}

func runCallhome(pctx context.Context, objAPI ObjectLayer) {
	fmt.Println("Inside runCallhome")
	// Make sure only 1 callhome is running on the cluster.
	locker := objAPI.NewNSLock(minioMetaBucket, "callhome/runCallhome.lock")
	lkctx, err := locker.GetLock(pctx, callhomeLeaderLockTimeout)
	if err != nil {
		// TODO: This may not be required
		if intDataUpdateTracker.debug {
			logger.LogIf(pctx, err)
		}
		fmt.Println("Returning because of error: ", err.Error())
		return
	}

	ctx := lkctx.Context()
	defer lkctx.Cancel()
	// No unlock for "leader" lock.

	callhomeTimer := time.NewTimer(callhomeCycle.Get())
	defer callhomeTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-callhomeTimer.C:
			// fmt.Println("Callhome timer triggered")
			// Reset the timer for next cycle.
			callhomeTimer.Reset(callhomeCycle.Get())

			if intDataUpdateTracker.debug {
				console.Debugln("starting callhome cycle")
			}

			go performCallhome(ctx)
		}
	}
}

func performCallhome(ctx context.Context) {
	m := GetSupportMetrics()
	resp, e := sendCallhomeMetrics(m)
	if e != nil {
		fmt.Println("Error from callhome: ", e.Error())
	} else {
		fmt.Println("Response from callhome: ", resp)
	}
}
